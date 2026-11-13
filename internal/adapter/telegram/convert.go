// Package telegram adapts a Telegram bot, through the Bot API's long polling,
// to the ConLoom Bot Interaction API. Only private chats are handled.
package telegram

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	platformTelegram = "telegram"
	// startGreeting replaces /start, which a user sends when opening the chat,
	// so the persona greets them in its own words.
	startGreeting = "你好"
)

// platformMessageID is the platform message ID of message id in chat:
// Telegram message IDs are only unique within a chat. The adapter ACKs sent
// messages with the same form, so the core's msg_<platform ID> of a bot reply
// matches the reply_to of a user quoting it.
func platformMessageID(chatID, messageID int64) string {
	return strconv.FormatInt(chatID, 10) + "_" + strconv.FormatInt(messageID, 10)
}

// toStandard converts a private message update into a standard Event and
// returns its idempotency key. An error means the update is skipped.
func toStandard(u update, self botUser, botInstanceID string) (conloomapi.Event, string, error) {
	m := u.Message
	switch {
	case m == nil:
		return conloomapi.Event{}, "", errors.New("not a new message")
	case m.Chat.Type != "private":
		return conloomapi.Event{}, "", fmt.Errorf("chat type %q is not private", m.Chat.Type)
	case m.From == nil:
		return conloomapi.Event{}, "", errors.New("message has no sender")
	case m.From.IsBot:
		return conloomapi.Event{}, "", errors.New("message from a bot")
	}
	text := messageText(m, self.Username)
	if strings.TrimSpace(text) == "" {
		return conloomapi.Event{}, "", errors.New("message has no supported content")
	}

	selfID := strconv.FormatInt(self.ID, 10)
	userID := strconv.FormatInt(m.From.ID, 10)
	pmid := platformMessageID(m.Chat.ID, m.MessageID)
	msg := conloomapi.Message{
		ID:                "msg_" + pmid,
		PlatformMessageID: pmid,
		Text:              text,
		Mentions:          []conloomapi.Mention{},
		Attachments:       []conloomapi.Attachment{},
	}
	if r := m.ReplyToMessage; r != nil {
		replyTo := "msg_" + platformMessageID(m.Chat.ID, r.MessageID)
		msg.ReplyTo = &replyTo
	}
	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_" + selfID + "_" + strconv.FormatInt(u.UpdateID, 10),
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: botInstanceID,
		Platform:      platformTelegram,
		OccurredAt:    time.Unix(m.Date, 0).UTC(),
		Conversation: conloomapi.Conversation{
			Type: conloomapi.ConvPrivate, ID: "private_" + userID, PlatformConversationID: strconv.FormatInt(m.Chat.ID, 10),
		},
		Actor: conloomapi.Actor{
			ID: "user_" + userID, PlatformUserID: userID, DisplayName: displayName(m.From), Roles: []string{"member"},
		},
		Trigger: conloomapi.Trigger{Type: conloomapi.TriggerPrivate, RequestedResponse: true},
		Message: msg,
		Extensions: map[string]any{
			"telegram": map[string]any{"update_id": u.UpdateID},
		},
	}
	return ev, "telegram:" + selfID + ":" + strconv.FormatInt(u.UpdateID, 10), nil
}

func displayName(u *botUser) string {
	if name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName)); name != "" {
		return name
	}
	return u.Username
}

func present(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

// messageText is the text of m: its text with commands normalised, or a
// placeholder for its media followed by the caption.
func messageText(m *message, botUsername string) string {
	if m.Text != "" {
		return normalizeCommand(m.Text, botUsername)
	}
	var placeholder string
	switch {
	case present(m.Photo):
		placeholder = "[图片]"
	case present(m.Sticker):
		placeholder = "[表情]"
	case present(m.Voice), present(m.Audio):
		placeholder = "[语音]"
	case present(m.Video), present(m.VideoNote), present(m.Animation):
		placeholder = "[视频]"
	case present(m.Document):
		placeholder = "[文件]"
	default:
		return ""
	}
	if caption := strings.TrimSpace(m.Caption); caption != "" {
		return placeholder + " " + caption
	}
	return placeholder
}

// normalizeCommand turns /start (with or without a payload) into a greeting
// and strips the @<bot username> suffix Telegram adds to commands chosen from
// the menu. Other text is returned unchanged.
func normalizeCommand(text, botUsername string) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}
	end := strings.IndexFunc(text, unicode.IsSpace)
	if end < 0 {
		end = len(text)
	}
	cmd, rest := text[:end], text[end:]
	if name, target, ok := strings.Cut(cmd, "@"); ok && botUsername != "" && strings.EqualFold(target, botUsername) {
		cmd = name
	}
	if cmd == "/start" {
		return startGreeting
	}
	return cmd + rest
}
