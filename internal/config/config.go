package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration

	ReferencePollInterval   time.Duration
	ReferenceRetryBaseDelay time.Duration
	ReferenceRetryMaxDelay  time.Duration
	ReferenceMaxAttempts    int

	AuthIssuer   string
	AuthJWKSURL  string
	AuthAudience string

	AWSRegion          string
	SQSEndpoint        string
	OutboxQueueURL     string
	OutboxPollInterval time.Duration
}

func Load() (Config, error) {
	return FromEnv(os.Getenv)
}

func FromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:    valueOr(getenv("HTTP_ADDR"), ":8080"),
		DatabaseURL: getenv("DATABASE_URL"),
		LogLevel:    slog.LevelInfo,

		AuthIssuer:   getenv("AUTH_ISSUER"),
		AuthJWKSURL:  getenv("AUTH_JWKS_URL"),
		AuthAudience: valueOr(getenv("AUTH_AUDIENCE"), "wagering-service"),

		AWSRegion:      valueOr(getenv("AWS_REGION"), "us-east-1"),
		SQSEndpoint:    getenv("SQS_ENDPOINT"),
		OutboxQueueURL: getenv("OUTBOX_QUEUE_URL"),
	}

	var errs []error

	required := []struct{ name, value string }{
		{"DATABASE_URL", cfg.DatabaseURL},
		{"AUTH_ISSUER", cfg.AuthIssuer},
		{"AUTH_JWKS_URL", cfg.AuthJWKSURL},
		{"OUTBOX_QUEUE_URL", cfg.OutboxQueueURL},
	}
	for _, r := range required {
		if r.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.name))
		}
	}

	if raw := getenv("LOG_LEVEL"); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
		}
	}

	durations := []struct {
		name     string
		target   *time.Duration
		fallback time.Duration
	}{
		{"SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout, 20 * time.Second},
		{"REFERENCE_POLL_INTERVAL", &cfg.ReferencePollInterval, time.Second},
		{"REFERENCE_RETRY_BASE_DELAY", &cfg.ReferenceRetryBaseDelay, time.Second},
		{"REFERENCE_RETRY_MAX_DELAY", &cfg.ReferenceRetryMaxDelay, time.Minute},
		{"OUTBOX_POLL_INTERVAL", &cfg.OutboxPollInterval, 500 * time.Millisecond},
	}
	for _, d := range durations {
		value, err := positiveDuration(d.name, getenv(d.name), d.fallback)
		if err != nil {
			errs = append(errs, err)
		}
		*d.target = value
	}

	cfg.ReferenceMaxAttempts = 10
	if raw := getenv("REFERENCE_MAX_ATTEMPTS"); raw != "" {
		attempts, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("REFERENCE_MAX_ATTEMPTS: %w", err))
		case attempts < 1:
			errs = append(errs, errors.New("REFERENCE_MAX_ATTEMPTS must be at least 1"))
		default:
			cfg.ReferenceMaxAttempts = attempts
		}
	}

	if cfg.ReferenceRetryMaxDelay < cfg.ReferenceRetryBaseDelay {
		errs = append(errs, errors.New("REFERENCE_RETRY_MAX_DELAY must not be shorter than REFERENCE_RETRY_BASE_DELAY"))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func positiveDuration(name, raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	switch {
	case err != nil:
		return fallback, fmt.Errorf("%s: %w", name, err)
	case value <= 0:
		return fallback, fmt.Errorf("%s must be positive", name)
	}
	return value, nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
