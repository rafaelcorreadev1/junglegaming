package observability

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"wagering-service/internal/application"
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

type ReconciliationReporter struct {
	logger *slog.Logger
	total  *prometheus.CounterVec
}

func NewReconciliationReporter(registry *prometheus.Registry, logger *slog.Logger) *ReconciliationReporter {
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wagering_reconciliations_total",
		Help: "Wallet reconciliations by result: consistent or divergent.",
	}, []string{"result"})
	registry.MustRegister(total)
	// Both series exist from the start, so a divergence shows up as an increase rather than as a new series.
	total.WithLabelValues("consistent")
	total.WithLabelValues("divergent")
	return &ReconciliationReporter{logger: logger, total: total}
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
		r.total.WithLabelValues("consistent").Inc()
		r.logger.DebugContext(ctx, "wallet reconciled", attrs...)
		return
	}
	r.total.WithLabelValues("divergent").Inc()
	r.logger.ErrorContext(ctx, "wallet balance diverges from its ledger", attrs...)
}
