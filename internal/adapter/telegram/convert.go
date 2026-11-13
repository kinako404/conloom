// Package telegram adapts a Telegram bot, through the Bot API's long polling,
// to the ConLoom Bot Interaction API. It handles private chats, groups and
// supergroups; channels are skipped.
package telegram

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

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
// matches the reply_to of a user quoting it. Group chat IDs are negative; the
// standard IDs keep the minus sign, which no ID check rejects.
func platformMessageID(chatID, messageID int64) string {
	return strconv.FormatInt(chatID, 10) + "_" + strconv.FormatInt(messageID, 10)
}

// toStandard converts a message update from a private chat, group or
// supergroup into a standard Event and returns its idempotency key. An error
// means the update is skipped. Whether the bot works in a group is left to
// the core.
func toStandard(u update, self botUser, botInstanceID string) (conloomapi.Event, string, error) {
	m := u.Message
	switch {
	case m == nil:
		return conloomapi.Event{}, "", errors.New("not a new message")
	case m.From == nil:
		return conloomapi.Event{}, "", errors.New("message has no sender")
	case m.From.IsBot:
		return conloomapi.Event{}, "", errors.New("message from a bot")
	}
	userID := strconv.FormatInt(m.From.ID, 10)
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	var conv conloomapi.Conversation
	switch m.Chat.Type {
	case "private":
		conv = conloomapi.Conversation{Type: conloomapi.ConvPrivate, ID: "private_" + userID, PlatformConversationID: chatID}
	case "group", "supergroup":
		conv = conloomapi.Conversation{Type: conloomapi.ConvGroup, ID: "group_" + chatID, PlatformConversationID: chatID}
	default:
		return conloomapi.Event{}, "", fmt.Errorf("chat type %q is not handled", m.Chat.Type)
	}
	c := readContent(m, self, botInstanceID)
	if strings.TrimSpace(c.text) == "" && len(c.mentions) == 0 {
		return conloomapi.Event{}, "", errors.New("message has no supported content")
	}

	selfID := strconv.FormatInt(self.ID, 10)
	pmid := platformMessageID(m.Chat.ID, m.MessageID)
	msg := conloomapi.Message{
		ID:                "msg_" + pmid,
		PlatformMessageID: pmid,
		Text:              c.text,
		Mentions:          c.mentions,
		Attachments:       []conloomapi.Attachment{},
	}
	if r := m.ReplyToMessage; r != nil {
		replyTo := "msg_" + platformMessageID(m.Chat.ID, r.MessageID)
		msg.ReplyTo = &replyTo
	}

	trigger := conloomapi.Trigger{Type: conloomapi.TriggerObserve}
	switch {
	case conv.Type == conloomapi.ConvPrivate:
		trigger.Type = conloomapi.TriggerPrivate
	case slices.ContainsFunc(msg.Mentions, func(m conloomapi.Mention) bool { return m.Type == "bot" }):
		trigger.Type = conloomapi.TriggerMention
	case msg.ReplyTo != nil:
		trigger.Type = conloomapi.TriggerReply
	case c.command:
		trigger.Type = conloomapi.TriggerCommand
	}
	trigger.RequestedResponse = trigger.Type != conloomapi.TriggerObserve

	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_" + selfID + "_" + strconv.FormatInt(u.UpdateID, 10),
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: botInstanceID,
		Platform:      platformTelegram,
		OccurredAt:    time.Unix(m.Date, 0).UTC(),
		Conversation:  conv,
		Actor: conloomapi.Actor{
			ID: "user_" + userID, PlatformUserID: userID, DisplayName: displayName(m.From), Roles: []string{"member"},
		},
		Trigger: trigger,
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

// content is what a message says: its text, whom it mentions, and whether it
// is a command for this bot.
type content struct {
	text     string
	mentions []conloomapi.Mention
	command  bool
}

// readContent reads m's text, or a placeholder for its media followed by the
// caption, and the mentions in either. Mentions of the bot are removed from
// the text, as the mention list carries them. Commands are normalised: see
// normalizeCommand; a /start addressed to the bot by name mentions it.
func readContent(m *message, self botUser, botInstanceID string) content {
	if m.Text != "" {
		text, mentions := extractMentions(m.Text, m.Entities, self, botInstanceID)
		text, toBot, command := normalizeCommand(text, self.Username)
		if toBot && text == startGreeting {
			mentions = appendMention(mentions, conloomapi.Mention{Type: "bot", ID: botInstanceID})
		}
		return content{text: text, mentions: mentions, command: command}
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
		return content{mentions: []conloomapi.Mention{}}
	}
	caption, mentions := extractMentions(m.Caption, m.CaptionEntities, self, botInstanceID)
	if caption = strings.TrimSpace(caption); caption != "" {
		return content{text: placeholder + " " + caption, mentions: mentions}
	}
	return content{text: placeholder, mentions: mentions}
}

func appendMention(ms []conloomapi.Mention, m conloomapi.Mention) []conloomapi.Mention {
	if slices.Contains(ms, m) {
		return ms
	}
	return append(ms, m)
}

// extractMentions returns the mentions in text's entities and text without the
// mentions of the bot (and one space after each). A mention entity naming the
// bot's username, or a text_mention of the bot, mentions the bot; others
// mention a member: by user ID for text_mention, else by username, which is
// all the entity carries. Entities out of range are ignored.
func extractMentions(text string, entities []entity, self botUser, botInstanceID string) (string, []conloomapi.Mention) {
	mentions := []conloomapi.Mention{}
	units := utf16.Encode([]rune(text))
	var kept []uint16
	last := 0
	for _, e := range entities {
		if e.Offset < last || e.Length <= 0 || e.Offset+e.Length > len(units) {
			continue
		}
		var m conloomapi.Mention
		switch e.Type {
		case "mention":
			username := strings.TrimPrefix(string(utf16.Decode(units[e.Offset:e.Offset+e.Length])), "@")
			if self.Username != "" && strings.EqualFold(username, self.Username) {
				m = conloomapi.Mention{Type: "bot", ID: botInstanceID}
			} else {
				m = conloomapi.Mention{Type: "user", ID: "username_" + strings.ToLower(username), Name: username}
			}
		case "text_mention":
			if e.User == nil {
				continue
			}
			if e.User.ID == self.ID {
				m = conloomapi.Mention{Type: "bot", ID: botInstanceID}
			} else {
				m = conloomapi.Mention{Type: "user", ID: "user_" + strconv.FormatInt(e.User.ID, 10), Name: displayName(e.User)}
			}
		default:
			continue
		}
		mentions = appendMention(mentions, m)
		if m.Type == "bot" {
			kept = append(kept, units[last:e.Offset]...)
			last = e.Offset + e.Length
			if last < len(units) && units[last] == ' ' {
				last++
			}
		}
	}
	if last == 0 {
		return text, mentions
	}
	kept = append(kept, units[last:]...)
	return strings.TrimSpace(string(utf16.Decode(kept))), mentions
}

// normalizeCommand handles a command at the start of text. /start (with or
// without a payload) becomes a greeting. A command with the @<bot username>
// suffix Telegram adds in groups loses the suffix (toBot reports it was
// there); a command naming another bot is not ours and stays as it is, so the
// core treats it as an ordinary message. command reports a command for this
// bot.
func normalizeCommand(text, botUsername string) (out string, toBot, command bool) {
	if !strings.HasPrefix(text, "/") {
		return text, false, false
	}
	end := strings.IndexFunc(text, unicode.IsSpace)
	if end < 0 {
		end = len(text)
	}
	cmd, rest := text[:end], text[end:]
	if name, target, ok := strings.Cut(cmd, "@"); ok {
		if botUsername == "" || !strings.EqualFold(target, botUsername) {
			return text, false, false
		}
		cmd, toBot = name, true
	}
	if cmd == "/start" {
		return startGreeting, toBot, false
	}
	return cmd + rest, toBot, true
}
