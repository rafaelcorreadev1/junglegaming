package observability

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
)

var Module = fx.Module("observability",
	fx.Provide(
		NewLogger,
		NewRegistry,
		NewMetrics,
		fx.Annotate(NewReconciliationReporter, fx.As(new(application.ReconciliationReporter))),
	),
)

func NewLogger(cfg config.Config) *slog.Logger {
	return slog.New(NewContextHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))
}

type fieldsKey struct{}

// fields is shared by pointer, so attributes added deep in a request reach logs written later by an outer layer.
type fields struct {
	mu    sync.Mutex
	attrs []slog.Attr
}

// WithFields returns a context whose logs carry the given key-value pairs, plus any the parent already had.
func WithFields(ctx context.Context, args ...any) context.Context {
	f := &fields{}
	if parent, ok := ctx.Value(fieldsKey{}).(*fields); ok {
		f.attrs = parent.snapshot()
	}
	f.add(args)
	return context.WithValue(ctx, fieldsKey{}, f)
}

// AddFields adds to the fields of a context created by WithFields; without one it does nothing.
func AddFields(ctx context.Context, args ...any) {
	if f, ok := ctx.Value(fieldsKey{}).(*fields); ok {
		f.add(args)
	}
}

func (f *fields) add(args []any) {
	record := slog.Record{}
	record.Add(args...)
	f.mu.Lock()
	defer f.mu.Unlock()
	record.Attrs(func(attr slog.Attr) bool {
		for i := range f.attrs {
			if f.attrs[i].Key == attr.Key {
				f.attrs[i] = attr
				return true
			}
		}
		f.attrs = append(f.attrs, attr)
		return true
	})
}

func (f *fields) snapshot() []slog.Attr {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]slog.Attr(nil), f.attrs...)
}

// ContextHandler adds the context's fields to every record, so callers only pass what is specific to the message.
type ContextHandler struct {
	next slog.Handler
}

func NewContextHandler(next slog.Handler) *ContextHandler {
	return &ContextHandler{next: next}
}

func (h *ContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *ContextHandler) Handle(ctx context.Context, record slog.Record) error {
	if f, ok := ctx.Value(fieldsKey{}).(*fields); ok {
		record = record.Clone()
		record.AddAttrs(f.snapshot()...)
	}
	return h.next.Handle(ctx, record)
}

func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{next: h.next.WithAttrs(attrs)}
}

func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{next: h.next.WithGroup(name)}
}
