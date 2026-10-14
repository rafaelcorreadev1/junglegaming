//go:build integration

// Package integration runs against the service started by docker compose up, with real Keycloak, PostgreSQL and LocalStack.
package integration

import (
	"strings"
	"testing"

	"github.com/rafaelcorreadev1/junglegaming/internal/testenv"
	"github.com/rafaelcorreadev1/junglegaming/test/e2e"
)

func newClient(t *testing.T) *e2e.Client {
	t.Helper()
	return e2e.NewClient(t, strings.Split(testenv.Lookup(t, "TEST_APP_URL", "http://localhost:8080"), ","))
}
