package store

import (
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// PanelScopeID is the conversation ID part of the memory scope key that all
// panel chats (PlatformPanel, private) of a bot share. Panel chat IDs start
// with "panel_", so no conversation has it.
const PanelScopeID = "~shared"

// MemoryScopeKey returns the key the memory of the conversation convKey is
// kept under (summary and cursor, member notes, affinity and forget times,
// and their revisions): for a private chat on PlatformPanel the bot's shared
// panel scope, "<bot>:panel:private:~shared"; for any other conversation
// convKey itself. A scope key is its own scope.
func MemoryScopeKey(convKey string) string {
	parts := strings.SplitN(convKey, ":", 4)
	if len(parts) == 4 && parts[1] == PlatformPanel && parts[2] == conloomapi.ConvPrivate {
		return ConversationKey(parts[0], PlatformPanel, conloomapi.ConvPrivate, PanelScopeID)
	}
	return convKey
}

// IsSharedScope reports whether key is a bot's shared panel memory scope.
func IsSharedScope(key string) bool {
	parts := strings.SplitN(key, ":", 4)
	return len(parts) == 4 && parts[1] == PlatformPanel && parts[2] == conloomapi.ConvPrivate && parts[3] == PanelScopeID
}
