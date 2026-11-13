package memory

import (
	"context"
	"errors"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ForgetStore is the part of store.Store that Forget uses.
type ForgetStore interface {
	ForgetMember(ctx context.Context, convKey, userID string, at time.Time) error
	GetGroupMemory(ctx context.Context, convKey string) (store.GroupMemory, error)
	PutGroupMemory(ctx context.Context, g store.GroupMemory) error
}

// Forget clears userID's note and affinity in convKey and marks them
// forgotten at at (store.ForgetMember). In a private conversation the summary
// is about that one person, so it is cleared too; the cursor is kept.
func Forget(ctx context.Context, st ForgetStore, convKey, userID string, at time.Time) error {
	if err := st.ForgetMember(ctx, convKey, userID, at); err != nil {
		return err
	}
	if convTypeOf(convKey) != conloomapi.ConvPrivate {
		return nil
	}
	g, err := st.GetGroupMemory(ctx, convKey)
	if errors.Is(err, store.ErrNotFound) || err == nil && g.Summary == "" {
		return nil
	}
	if err != nil {
		return err
	}
	g.Summary = ""
	return st.PutGroupMemory(ctx, g)
}
