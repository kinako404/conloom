package memory

import (
	"context"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ForgetStore is the part of store.Store that Forget uses.
type ForgetStore interface {
	ForgetMember(ctx context.Context, convKey, userID string, at time.Time) error
	ClearSummary(ctx context.Context, convKey string) error
}

// Forget clears userID's note and affinity in convKey and marks them
// forgotten at at (store.ForgetMember). In a private conversation the summary
// is about that one person, so it is cleared too, with all of its revisions;
// the cursor is kept. A group's summary is a shared record and is kept.
func Forget(ctx context.Context, st ForgetStore, convKey, userID string, at time.Time) error {
	if err := st.ForgetMember(ctx, convKey, userID, at); err != nil {
		return err
	}
	if convTypeOf(convKey) != conloomapi.ConvPrivate {
		return nil
	}
	return st.ClearSummary(ctx, convKey)
}
