package memory

import (
	"context"
	"slices"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// PanelScopeID is the conversation ID part of the memory scope key that all
// panel chats of a bot share (store.PanelScopeID).
const PanelScopeID = store.PanelScopeID

// ScopeKey returns the key conv's memory is kept under: its summary and
// cursor, the notes, affinity and forget times of its members and their
// revisions, the bot's status, bars and bar changes. Semantic search covers
// the chunks of every chat of the scope (ScopeChats). All private chats on
// store.PlatformPanel of a bot share one scope, "<bot>:panel:private:~shared"
// (store.PanelScopeKey), which parses like a key of a private panel chat;
// every other conversation is its own scope, keyed by conv.Key. Chat history
// and its chunks, personas, policies, tasks, outbox and timers stay with each
// conversation.
func ScopeKey(conv store.ConversationRecord) string {
	if conv.Platform == store.PlatformPanel && conv.Type == conloomapi.ConvPrivate {
		return store.PanelScopeKey(conv.BotInstanceID)
	}
	return conv.Key
}

// ScopeKeyOf is ScopeKey of the conversation whose key is convKey
// (store.MemoryScopeKey); a scope key is its own scope.
func ScopeKeyOf(convKey string) string {
	return store.MemoryScopeKey(convKey)
}

// IsSharedScope reports whether key is a bot's shared panel memory scope.
func IsSharedScope(key string) bool {
	return store.IsSharedScope(key)
}

// ScopeStore is the part of store.Store that ScopeChats uses.
type ScopeStore interface {
	PanelChatKeys(ctx context.Context, botID string) ([]string, error)
}

// ScopeChats returns the conversations whose memory scopeKey holds: for a
// shared panel scope every panel chat of its bot, ordered by key; for any
// other key that key alone.
func ScopeChats(ctx context.Context, st ScopeStore, scopeKey string) ([]string, error) {
	if !IsSharedScope(scopeKey) {
		return []string{scopeKey}, nil
	}
	return st.PanelChatKeys(ctx, consBotID(scopeKey))
}

// scope is a memory scope resolved for reading the messages it covers: its
// key and its chats. A conversation that is its own scope reads its messages
// as it always did.
type scope struct {
	key    string
	chats  []string
	shared bool
}

// resolveScope resolves the memory scope of convKey, a conversation's or a
// scope's key.
func resolveScope(ctx context.Context, st store.Store, convKey string) (scope, error) {
	key := ScopeKeyOf(convKey)
	chats, err := ScopeChats(ctx, st, key)
	if err != nil {
		return scope{}, err
	}
	return scope{key: key, chats: chats, shared: IsSharedScope(key)}, nil
}

// lead returns the chat whose policy and persona the scope's memory follows:
// for a shared scope the chat of its newest message, "" when none of its
// chats has one; otherwise the conversation itself.
func (sc scope) lead(ctx context.Context, st store.Store) (string, error) {
	if !sc.shared {
		return sc.key, nil
	}
	return st.LatestChat(ctx, sc.chats)
}

// multiChat reports whether the scope is shared by more than one chat, whose
// lines consolidation then marks with their chat (consChatLabels).
func (sc scope) multiChat() bool {
	return sc.shared && len(sc.chats) > 1
}

// has reports whether convKey is one of the scope's chats.
func (sc scope) has(convKey string) bool {
	return slices.Contains(sc.chats, convKey)
}

func (sc scope) messagesAfter(ctx context.Context, st store.Store, afterSeq int64, limit int) ([]store.MessageRecord, error) {
	if !sc.shared {
		return st.MessagesAfter(ctx, sc.key, afterSeq, limit)
	}
	return st.MessagesAfterIn(ctx, sc.chats, afterSeq, limit)
}

func (sc scope) countAfter(ctx context.Context, st store.Store, afterSeq int64) (int, error) {
	if !sc.shared {
		return st.CountMessagesAfter(ctx, sc.key, afterSeq)
	}
	return st.CountMessagesAfterIn(ctx, sc.chats, afterSeq)
}

func (sc scope) backlogCursor(ctx context.Context, st store.Store, backlog int) (int64, error) {
	if !sc.shared {
		return st.BacklogCursor(ctx, sc.key, backlog)
	}
	return st.BacklogCursorIn(ctx, sc.chats, backlog)
}
