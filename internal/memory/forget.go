package memory

import (
	"context"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ForgetStore is the part of store.Store that Forget uses.
type ForgetStore interface {
	ScopeStore
	ForgetMember(ctx context.Context, convKey, userID string, at time.Time) error
	ForgetPrivate(ctx context.Context, convKey, userID string, at time.Time) error
	ForgetPrivateScope(ctx context.Context, scopeKey string, chats []string, userID string, at time.Time) error
}

// Forget clears userID's note and affinity in the memory scope of convKey
// (ScopeKeyOf) and marks them forgotten at at (store.ForgetMember). A private
// conversation is about that one person, so it is forgotten whole
// (store.ForgetPrivate): its summary and summary revisions go too, and its
// cursor moves past everything said so far by either side; a shared panel
// scope is forgotten so with all its chats (store.ForgetPrivateScope). A
// group's summary is a shared record and is kept. Either way the semantic
// search chunks holding what was forgotten are deleted, and queued to be
// rebuilt from what is left.
func Forget(ctx context.Context, st ForgetStore, convKey, userID string, at time.Time) error {
	key := ScopeKeyOf(convKey)
	switch {
	case convTypeOf(key) != conloomapi.ConvPrivate:
		return st.ForgetMember(ctx, key, userID, at)
	case !IsSharedScope(key):
		return st.ForgetPrivate(ctx, key, userID, at)
	}
	chats, err := ScopeChats(ctx, st, key)
	if err != nil {
		return err
	}
	return st.ForgetPrivateScope(ctx, key, chats, userID, at)
}
