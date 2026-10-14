package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const readinessTimeout = 2 * time.Second

// ReadinessProbe checks one dependency the service cannot work without.
type ReadinessProbe interface {
	Name() string
	Ping(ctx context.Context) error
}

type HealthHandler struct {
	probes []ReadinessProbe
	logger *slog.Logger
}

func NewHealthHandler(probes []ReadinessProbe, logger *slog.Logger) *HealthHandler {
	return &HealthHandler{probes: probes, logger: logger}
}

func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	// In parallel, so one slow dependency does not eat the time budget of the others.
	checks := make(map[string]string, len(h.probes))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, probe := range h.probes {
		wg.Go(func() {
			state := "up"
			if err := probe.Ping(ctx); err != nil {
				state = "down"
				h.logger.WarnContext(ctx, "readiness check failed", "dependency", probe.Name(), "error", err)
			}
			mu.Lock()
			checks[probe.Name()] = state
			mu.Unlock()
		})
	}
	wg.Wait()

	for _, state := range checks {
		if state == "down" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "checks": checks})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "checks": checks})
}
