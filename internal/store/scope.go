package store

import (
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// PanelScopeID is the conversation ID part of the memory scope key that all
// panel chats (PlatformPanel, private) of a bot share. Panel chat IDs start
// with "panel_", so no conversation has it.
const PanelScopeID = "~shared"

// PanelScopeKey returns the memory scope key shared by the panel chats of
// botID, "<bot>:panel:private:~shared".
func PanelScopeKey(botID string) string {
	return ConversationKey(botID, PlatformPanel, conloomapi.ConvPrivate, PanelScopeID)
}

// MemoryScopeKey returns the key the memory of the conversation convKey is
// kept under (summary and cursor, member notes, affinity and forget times,
// and their revisions; the status, bars and bar changes): for a private chat
// on PlatformPanel the bot's shared panel scope (PanelScopeKey); for any
// other conversation convKey itself. A scope key is its own scope.
func MemoryScopeKey(convKey string) string {
	parts := strings.SplitN(convKey, ":", 4)
	if len(parts) == 4 && parts[1] == PlatformPanel && parts[2] == conloomapi.ConvPrivate {
		return PanelScopeKey(parts[0])
	}
	return convKey
}

// IsSharedScope reports whether key is a bot's shared panel memory scope.
func IsSharedScope(key string) bool {
	parts := strings.SplitN(key, ":", 4)
	return len(parts) == 4 && parts[1] == PlatformPanel && parts[2] == conloomapi.ConvPrivate && parts[3] == PanelScopeID
}

// ScopeMerge is what the migration of panel chat memory into shared scopes
// did for one bot: the panel chats whose memory it merged into the bot's
// scope, ordered by key.
type ScopeMerge struct {
	BotInstanceID string
	Chats         []string
}
