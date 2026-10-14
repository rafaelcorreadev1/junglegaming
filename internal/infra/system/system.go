// Package system provides the clock and the ID generator the application depends on.
package system

import (
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
)

var Module = fx.Module("system",
	fx.Provide(
		fx.Annotate(func() Clock { return Clock{} }, fx.As(new(application.Clock))),
		fx.Annotate(func() UUIDGenerator { return UUIDGenerator{} }, fx.As(new(application.IDGenerator))),
	),
)

type Clock struct{}

// Now is truncated to the microsecond, the precision PostgreSQL stores, so a value read back equals the one in memory.
func (Clock) Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

type UUIDGenerator struct{}

func (UUIDGenerator) NewID() string {
	// NewV7 fails only if the random source does; uuid.Must turns that into a panic instead of an ID collision risk.
	return uuid.Must(uuid.NewV7()).String()
}
