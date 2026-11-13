// Package usage records model and embedding calls (store.ModelCall) and
// summarizes them by local day.
package usage

import (
	"context"
	"errors"

	"github.com/kinako404/conloom/internal/store"
)

// Attribution is what a model call is made for. Fields are empty when
// unknown: consolidation has no task, background embedding no task either.
type Attribution struct{ BotInstanceID, ConversationKey, TaskID string }

type attributionKey struct{}

// WithAttribution returns ctx carrying a, for the calls made under it (e.g. a
// tool's query embedding during a reply task).
func WithAttribution(ctx context.Context, a Attribution) context.Context {
	return context.WithValue(ctx, attributionKey{}, a)
}

// AttributionFrom returns the attribution ctx carries, zero when absent.
func AttributionFrom(ctx context.Context) Attribution {
	a, _ := ctx.Value(attributionKey{}).(Attribution)
	return a
}

// Outcome classifies a call's error as store.OutcomeOK (nil),
// OutcomeCanceled, OutcomeTimeout (a deadline ran out or a network timeout)
// or OutcomeError.
func Outcome(err error) string {
	switch {
	case err == nil:
		return store.OutcomeOK
	case errors.Is(err, context.Canceled):
		return store.OutcomeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return store.OutcomeTimeout
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return store.OutcomeTimeout
	}
	return store.OutcomeError
}
