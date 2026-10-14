package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"
	"go.uber.org/goleak"

	"wagering-service/internal/config"
	"wagering-service/internal/infra/sqs"
	"wagering-service/internal/testdb"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// realConfig points the whole application at the test PostgreSQL schema, fresh LocalStack queues and a fake key server.
func realConfig(t *testing.T) config.Config {
	t.Helper()
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_SQS_ENDPOINT not set")
	}
	pool := testdb.NewPool(t)
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	client, err := sqs.NewClient(context.Background(), sqs.ClientConfig{Region: "us-east-1", Endpoint: endpoint})
	if err != nil {
		t.Fatalf("sqs client: %v", err)
	}
	queue := func(name string) string {
		created, err := client.CreateQueue(context.Background(), &awssqs.CreateQueueInput{
			QueueName:  aws.String(fmt.Sprintf("%s-%d.fifo", name, time.Now().UnixNano())),
			Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
		})
		if err != nil {
			t.Fatalf("create queue: %v", err)
		}
		t.Cleanup(func() {
			_, _ = client.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: created.QueueUrl})
		})
		return *created.QueueUrl
	}

	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"test"}]}`))
	}))
	t.Cleanup(keys.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	return config.Config{
		HTTPAddr:        addr,
		DatabaseURL:     os.Getenv("TEST_DATABASE_URL") + "&search_path=" + schema,
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: 10 * time.Second,

		ReferencePollInterval:   100 * time.Millisecond,
		ReferenceRetryBaseDelay: time.Second,
		ReferenceRetryMaxDelay:  time.Minute,
		ReferenceMaxAttempts:    10,

		AuthIssuer:   "http://issuer.test",
		AuthJWKSURL:  keys.URL,
		AuthAudience: "wagering-service",

		AWSRegion:          "us-east-1",
		SQSEndpoint:        endpoint,
		OutboxQueueURL:     queue("events"),
		OutboxPollInterval: 100 * time.Millisecond,

		WagerQueueURL:       queue("wagers"),
		WagerDLQURL:         queue("wagers-dlq"),
		WagerQueueProviders: []string{"provider-a"},
	}
}

// capturingLogger replaces the JSON logger, Fx's own events included, with one writing into logs.
func capturingLogger(logs *lockedBuffer) fx.Option {
	return fx.Decorate(func(*slog.Logger) *slog.Logger { return slog.New(slog.NewJSONHandler(logs, nil)) })
}

func TestApplicationStartsServesAndStopsWithoutLeakingGoroutines(t *testing.T) {
	cfg := realConfig(t)
	// Taken after the test's own pools, clients and servers exist: only what the application starts is checked.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	logs := &lockedBuffer{}
	app := fx.New(Options(cfg), capturingLogger(logs))
	startCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start: %v\n%s", err, logs)
	}

	response, err := http.Get("http://" + cfg.HTTPAddr + "/health/ready")
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("ready = %d %v", response.StatusCode, body)
	}
	http.DefaultClient.CloseIdleConnections()

	stopCtx, cancelStop := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelStop()
	start := time.Now()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v\n%s", err, logs)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("stop took %v with nothing in progress", elapsed)
	}

	order := stopOrder(t, logs.String())
	server, firstWorker, pool := order["httpapi.NewServer"], order["lifecycle.Attach"], order["postgres.NewPool"]
	if !(server < firstWorker && firstWorker < pool) {
		t.Errorf("stop order: http server %d, first worker %d, pool %d; want HTTP first, then workers, then the pool", server, firstWorker, pool)
	}
}

// stopOrder maps a hook's constructor to the position of its first OnStop.
func stopOrder(t *testing.T, logs string) map[string]int {
	t.Helper()
	order := map[string]int{}
	position := 0
	for _, raw := range strings.Split(strings.TrimSpace(logs), "\n") {
		var line struct {
			Msg    string `json:"msg"`
			Callee string `json:"callee"`
		}
		if json.Unmarshal([]byte(raw), &line) != nil || line.Msg != "OnStop hook executing" {
			continue
		}
		for _, name := range []string{"httpapi.NewServer", "lifecycle.Attach", "postgres.NewPool"} {
			if _, seen := order[name]; !seen && strings.Contains(line.Callee, name) {
				order[name] = position
			}
		}
		position++
	}
	if len(order) != 3 {
		t.Fatalf("stop hooks not found in the Fx log: %v", order)
	}
	return order
}

func TestStartFailsWhenADependencyIsMissing(t *testing.T) {
	cases := map[string]func(*config.Config){
		"missing queue": func(c *config.Config) { c.WagerDLQURL += "-missing" },
		"empty key set": func(c *config.Config) {
			empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"keys":[]}`))
			}))
			t.Cleanup(empty.Close)
			c.AuthJWKSURL = empty.URL
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := realConfig(t)
			breakIt(&cfg)
			app := fx.New(Options(cfg), capturingLogger(&lockedBuffer{}))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := app.Start(ctx); err == nil {
				_ = app.Stop(ctx)
				t.Fatal("start succeeded with a broken dependency")
			}
			if _, err := http.Get("http://" + cfg.HTTPAddr + "/health/live"); err == nil {
				t.Error("the HTTP server is listening although start failed")
			}
		})
	}
}
