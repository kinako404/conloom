package memory

import (
	"context"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ForgetStore is the part of store.Store that Forget uses.
type ForgetStore interface {
	ForgetMember(ctx context.Context, convKey, userID string, at time.Time) error
	ForgetPrivate(ctx context.Context, convKey, userID string, at time.Time) error
}

// Forget clears userID's note and affinity in convKey and marks them
// forgotten at at (store.ForgetMember). A private conversation is about that
// one person, so it is forgotten whole (store.ForgetPrivate): its summary and
// summary revisions go too, and its cursor moves past everything said so far
// by either side. A group's summary is a shared record and is kept.
func Forget(ctx context.Context, st ForgetStore, convKey, userID string, at time.Time) error {
	if convTypeOf(convKey) != conloomapi.ConvPrivate {
		return st.ForgetMember(ctx, convKey, userID, at)
	}
	return st.ForgetPrivate(ctx, convKey, userID, at)
}
