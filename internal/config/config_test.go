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
		"DATABASE_URL": "postgres://localhost/wagering",
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
}

func TestFromEnvReadsOverrides(t *testing.T) {
	cfg, err := FromEnv(envFrom(map[string]string{
		"DATABASE_URL":     "postgres://localhost/wagering",
		"HTTP_ADDR":        ":9090",
		"LOG_LEVEL":        "debug",
		"SHUTDOWN_TIMEOUT": "5s",
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
			env:     map[string]string{"DATABASE_URL": "x", "LOG_LEVEL": "loud"},
			wantErr: "LOG_LEVEL",
		},
		{
			name:    "malformed shutdown timeout",
			env:     map[string]string{"DATABASE_URL": "x", "SHUTDOWN_TIMEOUT": "soon"},
			wantErr: "SHUTDOWN_TIMEOUT",
		},
		{
			name:    "non-positive shutdown timeout",
			env:     map[string]string{"DATABASE_URL": "x", "SHUTDOWN_TIMEOUT": "0s"},
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
	for _, want := range []string{"DATABASE_URL", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}
