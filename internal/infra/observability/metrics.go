package observability

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
)

const (
	ChannelHTTP            = "http"
	ChannelSQS             = "sqs"
	ChannelReferenceWorker = "reference_worker"

	outboxAgeTimeout = 2 * time.Second
)

// NewRegistry is a registry of our own rather than the global default, so tests get a clean one each time.
func NewRegistry() *prometheus.Registry {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return registry
}

type Metrics struct {
	transactions    *prometheus.CounterVec
	duplicates      *prometheus.CounterVec
	retries         *prometheus.CounterVec
	published       prometheus.Counter
	deadLetters     *prometheus.CounterVec
	conflicts       *prometheus.CounterVec
	processing      *prometheus.HistogramVec
	httpRequests    *prometheus.CounterVec
	httpDuration    *prometheus.HistogramVec
	reconciliations *prometheus.CounterVec
}

func NewMetrics(registry *prometheus.Registry, outbox application.OutboxRepository, logger *slog.Logger) *Metrics {
	m := &Metrics{
		transactions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wagering_transactions_total",
			Help: "Wager transactions decided, by entry channel, kind and resulting status. Replays are not counted.",
		}, []string{"channel", "kind", "status"}),
		duplicates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wagering_duplicates_total",
			Help: "Repeated deliveries recognized: idempotent replays and messages already in the inbox.",
		}, []string{"channel", "kind"}),
		retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wagering_retries_total",
			Help: "Work scheduled to run again after a failure, by component.",
		}, []string{"component"}),
		published: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wagering_outbox_published_total",
			Help: "Events this instance sent to the queue. A republished event counts again.",
		}),
		deadLetters: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wagering_dlq_messages_total",
			Help: "Messages this service moved to the dead-letter queue, by reason. Moves made by the redrive policy happen in SQS and are not counted here.",
		}, []string{"reason"}),
		conflicts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wagering_concurrency_conflicts_total",
			Help: "Transactions that lost a race with another writer, by kind of conflict.",
		}, []string{"reason"}),
		processing: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "wagering_processing_duration_seconds",
			Help:    "Time to process one wager operation, replays included, by entry channel.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"channel"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wagering_http_requests_total",
			Help: "HTTP requests by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "wagering_http_request_duration_seconds",
			Help:    "HTTP request latency by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		reconciliations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wagering_reconciliations_total",
			Help: "Wallet reconciliations by result: consistent or divergent.",
		}, []string{"result"}),
	}
	outboxAge := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "wagering_outbox_oldest_pending_age_seconds",
		Help: "Age of the oldest unpublished outbox event; 0 when nothing is pending.",
	}, func() float64 { return oldestPendingAge(outbox, logger) })

	registry.MustRegister(m.transactions, m.duplicates, m.retries, m.published, m.deadLetters, m.conflicts,
		m.processing, m.httpRequests, m.httpDuration, m.reconciliations, outboxAge)
	// Both series exist from the start, so a divergence shows up as an increase rather than as a new series.
	m.reconciliations.WithLabelValues("consistent")
	m.reconciliations.WithLabelValues("divergent")
	return m
}

// oldestPendingAge runs on every scrape; a failed query yields NaN, which Prometheus stores as "no value" rather than a fake 0.
func oldestPendingAge(outbox application.OutboxRepository, logger *slog.Logger) float64 {
	ctx, cancel := context.WithTimeout(context.Background(), outboxAgeTimeout)
	defer cancel()
	oldest, pending, err := outbox.OldestPending(ctx)
	if err != nil {
		logger.WarnContext(ctx, "outbox age metric unavailable", "error", err)
		return math.NaN()
	}
	if !pending {
		return 0
	}
	return max(time.Since(oldest).Seconds(), 0)
}

// ObserveWager records a processed operation; a replay counts as a duplicate, not as a new transaction.
func (m *Metrics) ObserveWager(channel string, tx *domain.WagerTransaction, replay bool, elapsed time.Duration) {
	m.processing.WithLabelValues(channel).Observe(elapsed.Seconds())
	if replay {
		m.duplicates.WithLabelValues(channel, "idempotent_replay").Inc()
		return
	}
	m.TransactionDecided(channel, tx)
}

func (m *Metrics) TransactionDecided(channel string, tx *domain.WagerTransaction) {
	m.transactions.WithLabelValues(channel, string(tx.Kind()), string(tx.Status())).Inc()
}

func (m *Metrics) ObserveProcessing(channel string, elapsed time.Duration) {
	m.processing.WithLabelValues(channel).Observe(elapsed.Seconds())
}

func (m *Metrics) InboxDuplicate() {
	m.duplicates.WithLabelValues(ChannelSQS, "inbox").Inc()
}

func (m *Metrics) Retry(component string) {
	m.retries.WithLabelValues(component).Inc()
}

func (m *Metrics) Published() {
	m.published.Inc()
}

func (m *Metrics) DeadLettered(reason string) {
	m.deadLetters.WithLabelValues(reason).Inc()
}

func (m *Metrics) Conflict(reason string) {
	m.conflicts.WithLabelValues(reason).Inc()
}

func (m *Metrics) ObserveHTTP(method, route string, status int, elapsed time.Duration) {
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

type ReconciliationReporter struct {
	logger  *slog.Logger
	metrics *Metrics
}

func NewReconciliationReporter(metrics *Metrics, logger *slog.Logger) *ReconciliationReporter {
	return &ReconciliationReporter{logger: logger, metrics: metrics}
}

func (r *ReconciliationReporter) Report(ctx context.Context, reconciliation application.Reconciliation) {
	attrs := []any{
		"walletId", reconciliation.WalletID,
		"storedBalance", reconciliation.StoredBalance.String(),
		"calculatedBalance", reconciliation.CalculatedBalance.String(),
		"difference", reconciliation.Difference.String(),
		"checkedEntries", reconciliation.CheckedEntries,
	}
	if reconciliation.Consistent() {
		r.metrics.reconciliations.WithLabelValues("consistent").Inc()
		r.logger.DebugContext(ctx, "wallet reconciled", attrs...)
		return
	}
	r.metrics.reconciliations.WithLabelValues("divergent").Inc()
	r.logger.ErrorContext(ctx, "wallet balance diverges from its ledger", attrs...)
}
