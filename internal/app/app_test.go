package app

import (
	"testing"
	"time"

	"go.uber.org/fx"

	"wagering-service/internal/config"
)

func TestOptionsBuildAValidDependencyGraph(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:        "127.0.0.1:0",
		DatabaseURL:     "postgres://user:pass@127.0.0.1:5432/wagering",
		ShutdownTimeout: time.Second,

		ReferencePollInterval:   time.Second,
		ReferenceRetryBaseDelay: time.Second,
		ReferenceRetryMaxDelay:  time.Minute,
		ReferenceMaxAttempts:    10,
		ReferenceLease:          30 * time.Second,

		AuthIssuer:   "http://127.0.0.1:8081/realms/wagering",
		AuthJWKSURL:  "http://127.0.0.1:8081/realms/wagering/protocol/openid-connect/certs",
		AuthAudience: "wagering-service",

		AWSRegion:          "us-east-1",
		SQSEndpoint:        "http://127.0.0.1:4566",
		OutboxQueueURL:     "http://127.0.0.1:4566/000000000000/wager-events.fifo",
		OutboxLease:        30 * time.Second,
		OutboxPollInterval: time.Second,

		WagerQueueURL:          "http://127.0.0.1:4566/000000000000/wager-transactions.fifo",
		WagerDLQURL:            "http://127.0.0.1:4566/000000000000/wager-transactions-dlq.fifo",
		WagerQueueProviders:    []string{"provider-a"},
		WagerVisibilityTimeout: 30 * time.Second,
	}

	if err := fx.ValidateApp(Options(cfg)); err != nil {
		t.Fatalf("invalid fx graph: %v", err)
	}
}
