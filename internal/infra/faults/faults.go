// Package faults kills the process at the points where a crash is hardest to recover from, so tests can prove recovery with a real process.
package faults

import (
	"context"
	"log/slog"
	"os"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/config"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/sqs"
)

// ExitCode tells an injected crash apart from any other failure.
const ExitCode = 3

var exit = os.Exit

// Handler exits after the message's transaction committed and before the consumer deletes it from the queue.
func Handler(next sqs.WagerHandler, cfg config.Config, logger *slog.Logger) sqs.WagerHandler {
	if cfg.FaultInjection != config.FaultExitAfterCommit {
		return next
	}
	return &handler{next: next, logger: logger}
}

type handler struct {
	next   sqs.WagerHandler
	logger *slog.Logger
}

func (h *handler) Execute(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
	result, err := h.next.Execute(ctx, msg)
	if err == nil && !result.AlreadyHandled {
		h.logger.WarnContext(ctx, "fault injection: exiting after commit, before deleting the message", "messageId", msg.MessageID)
		exit(ExitCode)
	}
	return result, err
}

// Publisher exits after the event reached the queue and before the outbox marks it as published.
func Publisher(next application.EventPublisher, cfg config.Config, logger *slog.Logger) application.EventPublisher {
	if cfg.FaultInjection != config.FaultExitAfterPublish {
		return next
	}
	return &publisher{next: next, logger: logger}
}

type publisher struct {
	next   application.EventPublisher
	logger *slog.Logger
}

func (p *publisher) Publish(ctx context.Context, message application.OutboxMessage) error {
	if err := p.next.Publish(ctx, message); err != nil {
		return err
	}
	p.logger.WarnContext(ctx, "fault injection: exiting after publishing, before marking the event", "eventId", message.ID)
	exit(ExitCode)
	return nil
}
