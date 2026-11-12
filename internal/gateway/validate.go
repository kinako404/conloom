package gateway

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const maxTextRunes = 16000

// validateEvent reports every problem of ev as submitted for bot, joined by "; ".
func validateEvent(ev conloomapi.Event, bot config.BotConfig) error {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	if ev.SchemaVersion != conloomapi.SchemaVersion {
		fail("schema_version must be %q, got %q", conloomapi.SchemaVersion, ev.SchemaVersion)
	}
	if ev.EventType != conloomapi.EventTypeMessageCreated {
		fail("event_type must be %q, got %q", conloomapi.EventTypeMessageCreated, ev.EventType)
	}
	if ev.Conversation.Type != conloomapi.ConvGroup && ev.Conversation.Type != conloomapi.ConvPrivate {
		fail("conversation.type must be one of group|private, got %q", ev.Conversation.Type)
	}
	for _, f := range []struct{ path, v string }{
		{"conversation.id", ev.Conversation.ID},
		{"conversation.platform_conversation_id", ev.Conversation.PlatformConversationID},
		{"actor.id", ev.Actor.ID},
		{"actor.platform_user_id", ev.Actor.PlatformUserID},
		{"message.id", ev.Message.ID},
	} {
		if strings.TrimSpace(f.v) == "" {
			fail("%s must not be empty", f.path)
		}
	}
	if ev.Platform != bot.Platform {
		fail("platform must be %q for bot %q, got %q", bot.Platform, bot.ID, ev.Platform)
	}
	if n := utf8.RuneCountInString(ev.Message.Text); n > maxTextRunes {
		fail("message.text must be at most %d characters, got %d", maxTextRunes, n)
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}
