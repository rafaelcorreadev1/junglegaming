package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestFromEnvAppliesDefaults(t *testing.T) {
	cfg, err := FromEnv(envFrom(map[string]string{
		"DATABASE_URL":  "postgres://localhost/wagering",
		"AUTH_ISSUER":   "http://keycloak/realms/wagering",
		"AUTH_JWKS_URL": "http://keycloak/certs",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	if cfg.ShutdownTimeout != 20*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 20s", cfg.ShutdownTimeout)
	}
	if cfg.AuthAudience != "wagering-service" {
		t.Errorf("AuthAudience = %q, want wagering-service", cfg.AuthAudience)
	}
	if cfg.ReferencePollInterval != time.Second || cfg.ReferenceRetryBaseDelay != time.Second ||
		cfg.ReferenceRetryMaxDelay != time.Minute || cfg.ReferenceMaxAttempts != 10 {
		t.Errorf("reference retry defaults = %v/%v/%v/%d, want 1s/1s/1m/10",
			cfg.ReferencePollInterval, cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay, cfg.ReferenceMaxAttempts)
	}
}

func TestFromEnvReadsOverrides(t *testing.T) {
	cfg, err := FromEnv(envFrom(map[string]string{
		"DATABASE_URL":     "postgres://localhost/wagering",
		"AUTH_ISSUER":      "http://keycloak/realms/wagering",
		"AUTH_JWKS_URL":    "http://keycloak/certs",
		"AUTH_AUDIENCE":    "custom-audience",
		"HTTP_ADDR":        ":9090",
		"LOG_LEVEL":        "debug",
		"SHUTDOWN_TIMEOUT": "5s",

		"REFERENCE_POLL_INTERVAL":    "250ms",
		"REFERENCE_RETRY_BASE_DELAY": "2s",
		"REFERENCE_RETRY_MAX_DELAY":  "30s",
		"REFERENCE_MAX_ATTEMPTS":     "4",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":9090")
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 5s", cfg.ShutdownTimeout)
	}
	if cfg.AuthIssuer != "http://keycloak/realms/wagering" || cfg.AuthJWKSURL != "http://keycloak/certs" || cfg.AuthAudience != "custom-audience" {
		t.Errorf("auth = %q/%q/%q", cfg.AuthIssuer, cfg.AuthJWKSURL, cfg.AuthAudience)
	}
	if cfg.ReferencePollInterval != 250*time.Millisecond || cfg.ReferenceRetryBaseDelay != 2*time.Second ||
		cfg.ReferenceRetryMaxDelay != 30*time.Second || cfg.ReferenceMaxAttempts != 4 {
		t.Errorf("reference retry = %v/%v/%v/%d, want 250ms/2s/30s/4",
			cfg.ReferencePollInterval, cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay, cfg.ReferenceMaxAttempts)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "missing database url",
			env:     map[string]string{},
			wantErr: "DATABASE_URL is required",
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": "x", "AUTH_JWKS_URL": "x", "LOG_LEVEL": "loud"},
			wantErr: "LOG_LEVEL",
		},
		{
			name:    "malformed shutdown timeout",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": "x", "AUTH_JWKS_URL": "x", "SHUTDOWN_TIMEOUT": "soon"},
			wantErr: "SHUTDOWN_TIMEOUT",
		},
		{
			name:    "malformed reference poll interval",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": "x", "AUTH_JWKS_URL": "x", "REFERENCE_POLL_INTERVAL": "often"},
			wantErr: "REFERENCE_POLL_INTERVAL",
		},
		{
			name:    "non-positive reference base delay",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": "x", "AUTH_JWKS_URL": "x", "REFERENCE_RETRY_BASE_DELAY": "-1s"},
			wantErr: "REFERENCE_RETRY_BASE_DELAY must be positive",
		},
		{
			name:    "reference max delay below the base delay",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": "x", "AUTH_JWKS_URL": "x", "REFERENCE_RETRY_BASE_DELAY": "10s", "REFERENCE_RETRY_MAX_DELAY": "5s"},
			wantErr: "REFERENCE_RETRY_MAX_DELAY must not be shorter",
		},
		{
			name:    "malformed reference max attempts",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": "x", "AUTH_JWKS_URL": "x", "REFERENCE_MAX_ATTEMPTS": "many"},
			wantErr: "REFERENCE_MAX_ATTEMPTS",
		},
		{
			name:    "zero reference max attempts",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": "x", "AUTH_JWKS_URL": "x", "REFERENCE_MAX_ATTEMPTS": "0"},
			wantErr: "REFERENCE_MAX_ATTEMPTS must be at least 1",
		},
		{
			name:    "non-positive shutdown timeout",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": "x", "AUTH_JWKS_URL": "x", "SHUTDOWN_TIMEOUT": "0s"},
			wantErr: "SHUTDOWN_TIMEOUT must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromEnv(envFrom(tt.env))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestFromEnvReportsEveryProblemAtOnce(t *testing.T) {
	_, err := FromEnv(envFrom(map[string]string{"LOG_LEVEL": "loud"}))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, want := range []string{"DATABASE_URL", "AUTH_ISSUER", "AUTH_JWKS_URL", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}
