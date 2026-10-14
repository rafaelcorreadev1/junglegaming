// Package testenv tells tests where the real PostgreSQL and LocalStack are.
//
// Without the integration build tag a test that needs them is skipped when its variable is unset.
// With -tags=integration they are mandatory and default to the Compose ports, so a missing container fails the run instead of hiding it.
package testenv

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	composeDatabaseURL = "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable"
	composeSQSEndpoint = "http://localhost:4566"
)

func DatabaseURL(t testing.TB) string {
	t.Helper()
	return Lookup(t, "TEST_DATABASE_URL", composeDatabaseURL)
}

func SQSEndpoint(t testing.TB) string {
	t.Helper()
	return Lookup(t, "TEST_SQS_ENDPOINT", composeSQSEndpoint)
}

// Lookup reads name, falling back to fallback in an integration run and skipping the test otherwise.
func Lookup(t testing.TB, name, fallback string) string {
	t.Helper()
	if value := os.Getenv(name); value != "" {
		return value
	}
	if !Integration {
		t.Skipf("%s is not set; run with -tags=integration to use the Compose defaults", name)
	}
	return fallback
}

// UniqueName adds a random suffix; a clock-based one repeats when tests start in the same tick, as on Windows.
func UniqueName(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}
