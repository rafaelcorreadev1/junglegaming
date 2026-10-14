//go:build integration

// Package integration runs against the service started by docker compose up, with real Keycloak, PostgreSQL and LocalStack.
package integration

import (
	"strings"
	"testing"

	"wagering-service/internal/testenv"
	"wagering-service/test/e2e"
)

func newClient(t *testing.T) *e2e.Client {
	t.Helper()
	return e2e.NewClient(t, strings.Split(testenv.Lookup(t, "TEST_APP_URL", "http://localhost:8080"), ","))
}
