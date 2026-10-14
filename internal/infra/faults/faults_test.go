package faults

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/config"
)

type stubHandler struct {
	result application.HandleWagerMessageResult
	err    error
}

func (s stubHandler) Execute(context.Context, application.WagerMessage) (application.HandleWagerMessageResult, error) {
	return s.result, s.err
}

type stubPublisher struct{ err error }

func (s stubPublisher) Publish(context.Context, application.OutboxMessage) error { return s.err }

func recordExit(t *testing.T) *[]int {
	t.Helper()
	var codes []int
	exit = func(code int) { codes = append(codes, code) }
	t.Cleanup(func() { exit = nil })
	return &codes
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestHandlerExitsOnlyAfterAFreshCommit(t *testing.T) {
	cfg := config.Config{FaultInjection: config.FaultExitAfterCommit}
	cases := map[string]struct {
		next     stubHandler
		wantExit bool
	}{
		"committed":       {stubHandler{}, true},
		"already handled": {stubHandler{result: application.HandleWagerMessageResult{AlreadyHandled: true}}, false},
		"failed":          {stubHandler{err: errors.New("down")}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			codes := recordExit(t)
			_, _ = Handler(c.next, cfg, discard).Execute(context.Background(), application.WagerMessage{})
			if got := len(*codes) == 1 && (*codes)[0] == ExitCode; got != c.wantExit {
				t.Errorf("exits = %v, want exit %v", *codes, c.wantExit)
			}
		})
	}
}

func TestPublisherExitsOnlyAfterASuccessfulPublish(t *testing.T) {
	cfg := config.Config{FaultInjection: config.FaultExitAfterPublish}
	codes := recordExit(t)
	_ = Publisher(stubPublisher{err: errors.New("down")}, cfg, discard).Publish(context.Background(), application.OutboxMessage{})
	if len(*codes) != 0 {
		t.Fatalf("exited after a failed publish: %v", *codes)
	}
	_ = Publisher(stubPublisher{}, cfg, discard).Publish(context.Background(), application.OutboxMessage{})
	if len(*codes) != 1 || (*codes)[0] != ExitCode {
		t.Errorf("exits = %v, want one exit with %d", *codes, ExitCode)
	}
}

func TestNoFaultLeavesTheDependencyUntouched(t *testing.T) {
	next, pub := stubHandler{}, stubPublisher{}
	if Handler(next, config.Config{FaultInjection: config.FaultExitAfterPublish}, discard) != next {
		t.Error("handler wrapped for another fault")
	}
	if Publisher(pub, config.Config{}, discard) != pub {
		t.Error("publisher wrapped without a fault")
	}
}
