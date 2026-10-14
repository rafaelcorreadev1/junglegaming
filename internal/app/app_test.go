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

		AuthIssuer:   "http://127.0.0.1:8081/realms/wagering",
		AuthJWKSURL:  "http://127.0.0.1:8081/realms/wagering/protocol/openid-connect/certs",
		AuthAudience: "wagering-service",
	}

	if err := fx.ValidateApp(Options(cfg)); err != nil {
		t.Fatalf("invalid fx graph: %v", err)
	}
}
