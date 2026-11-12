// Package napcat adapts a NapCat (OneBot 11) QQ client to the ConLoom Bot Interaction API.
package napcat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const platformQQ = "qq"

var ErrUnsupportedAction = errors.New("napcat: unsupported action type")

type OBSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type OBSender struct {
	UserID   json.Number `json:"user_id"`
	Nickname string      `json:"nickname"`
	Card     string      `json:"card"`
	Role     string      `json:"role"`
}

type OBEvent struct {
	Time        int64           `json:"time"`
	SelfID      json.Number     `json:"self_id"`
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	MessageID   json.Number     `json:"message_id"`
	UserID      json.Number     `json:"user_id"`
	GroupID     json.Number     `json:"group_id"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message"`
	Sender      OBSender        `json:"sender"`
}

func ParseEvent(raw []byte) (OBEvent, error) {
	var ev OBEvent
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&ev); err != nil {
		return OBEvent{}, fmt.Errorf("napcat: decode event: %w", err)
	}
	return ev, nil
}

// ParseSegments accepts both OneBot message formats: a segment array or a CQ-code string.
func ParseSegments(msg json.RawMessage) ([]OBSegment, error) {
	trimmed := bytes.TrimSpace(msg)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	switch trimmed[0] {
	case '[':
		var segs []OBSegment
		if err := dec.Decode(&segs); err != nil {
			return nil, fmt.Errorf("napcat: decode message segments: %w", err)
		}
		return segs, nil
	case '"':
		var s string
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("napcat: decode message string: %w", err)
		}
		return parseCQ(s), nil
	default:
		return nil, fmt.Errorf("napcat: unsupported message format %q", trimmed[:1])
	}
}

var cqUnescaper = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&#44;", ",", "&amp;", "&")

func parseCQ(s string) []OBSegment {
	var segs []OBSegment
	addText := func(t string) {
		if t != "" {
			segs = append(segs, OBSegment{Type: "text", Data: map[string]any{"text": cqUnescaper.Replace(t)}})
		}
	}
	for {
		start := strings.Index(s, "[CQ:")
		if start < 0 {
			break
		}
		end := strings.IndexByte(s[start:], ']')
		if end < 0 {
			break
		}
		addText(s[:start])
		parts := strings.Split(s[start+len("[CQ:"):start+end], ",")
		data := make(map[string]any, len(parts)-1)
		for _, p := range parts[1:] {
			if k, v, ok := strings.Cut(p, "="); ok {
				data[k] = cqUnescaper.Replace(v)
			}
		}
		segs = append(segs, OBSegment{Type: parts[0], Data: data})
		s = s[start+end+1:]
	}
	addText(s)
	return segs
}

func dataString(data map[string]any, key string) string {
	switch v := data[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func isNumericID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ToStandard converts a OneBot message event into a standard Event and returns its idempotency key.
func ToStandard(ev OBEvent, botInstanceID string) (conloomapi.Event, string, error) {
	if ev.PostType != "message" {
		return conloomapi.Event{}, "", fmt.Errorf("napcat: unsupported post_type %q", ev.PostType)
	}
	selfID, msgID, userID := ev.SelfID.String(), ev.MessageID.String(), ev.UserID.String()
	for name, v := range map[string]string{"self_id": selfID, "message_id": msgID, "user_id": userID} {
		if !isNumericID(v) {
			return conloomapi.Event{}, "", fmt.Errorf("napcat: invalid %s %q", name, v)
		}
	}

	var conv conloomapi.Conversation
	switch ev.MessageType {
	case "group":
		groupID := ev.GroupID.String()
		if !isNumericID(groupID) {
			return conloomapi.Event{}, "", fmt.Errorf("napcat: invalid group_id %q", groupID)
		}
		conv = conloomapi.Conversation{Type: conloomapi.ConvGroup, ID: "group_" + groupID, PlatformConversationID: groupID}
	case "private":
		conv = conloomapi.Conversation{Type: conloomapi.ConvPrivate, ID: "private_" + userID, PlatformConversationID: userID}
	default:
		return conloomapi.Event{}, "", fmt.Errorf("napcat: unsupported message_type %q", ev.MessageType)
	}

	segs, err := ParseSegments(ev.Message)
	if err != nil {
		return conloomapi.Event{}, "", err
	}

	msg := conloomapi.Message{
		ID:                "msg_" + msgID,
		PlatformMessageID: msgID,
		Mentions:          []conloomapi.Mention{},
		Attachments:       []conloomapi.Attachment{},
	}
	var text strings.Builder
	mentionsBot := false
	for _, seg := range segs {
		switch seg.Type {
		case "text":
			text.WriteString(dataString(seg.Data, "text"))
		case "at":
			switch qq := dataString(seg.Data, "qq"); qq {
			case selfID:
				mentionsBot = true
				msg.Mentions = append(msg.Mentions, conloomapi.Mention{Type: "bot", ID: botInstanceID})
			case "all":
				msg.Mentions = append(msg.Mentions, conloomapi.Mention{Type: "all", ID: "all"})
			default:
				msg.Mentions = append(msg.Mentions, conloomapi.Mention{Type: "user", ID: "user_" + qq})
			}
		case "reply":
			replyTo := "msg_" + dataString(seg.Data, "id")
			msg.ReplyTo = &replyTo
		case "image":
			msg.Attachments = append(msg.Attachments, conloomapi.Attachment{
				Type: "image",
				URL:  dataString(seg.Data, "url"),
				Name: dataString(seg.Data, "file"),
			})
		case "face", "mface":
			text.WriteString("[表情]")
		case "record":
			text.WriteString("[语音]")
		case "video":
			text.WriteString("[视频]")
		case "file":
			text.WriteString("[文件]")
		}
	}
	msg.Text = strings.TrimSpace(text.String())

	trigger := conloomapi.Trigger{Type: conloomapi.TriggerObserve}
	switch {
	case conv.Type == conloomapi.ConvPrivate:
		trigger.Type = conloomapi.TriggerPrivate
	case mentionsBot:
		trigger.Type = conloomapi.TriggerMention
	case msg.ReplyTo != nil:
		trigger.Type = conloomapi.TriggerReply
	case strings.HasPrefix(msg.Text, "/"):
		trigger.Type = conloomapi.TriggerCommand
	}
	trigger.RequestedResponse = trigger.Type != conloomapi.TriggerObserve

	displayName := ev.Sender.Card
	if displayName == "" {
		displayName = ev.Sender.Nickname
	}
	role := ev.Sender.Role
	if role == "" {
		role = "member"
	}

	out := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_" + selfID + "_" + msgID,
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: botInstanceID,
		Platform:      platformQQ,
		OccurredAt:    time.Unix(ev.Time, 0).UTC(),
		Conversation:  conv,
		Actor: conloomapi.Actor{
			ID:             "user_" + userID,
			PlatformUserID: userID,
			DisplayName:    displayName,
			Roles:          []string{role},
		},
		Trigger: trigger,
		Message: msg,
		Extensions: map[string]any{
			"napcat": map[string]any{"self_id": selfID, "sub_type": ev.SubType},
		},
	}
	return out, "napcat:" + selfID + ":" + msgID, nil
}

// SendRequest builds the OneBot API call for an outbox action. Text is always sent
// as a text segment, never as a CQ string, so user content cannot inject CQ codes.
func SendRequest(a conloomapi.Action) (action string, params map[string]any, err error) {
	if a.Type != conloomapi.ActionMessageSend {
		return "", nil, fmt.Errorf("%w: %q", ErrUnsupportedAction, a.Type)
	}
	var idKey string
	switch a.ConversationType {
	case conloomapi.ConvGroup:
		action, idKey = "send_group_msg", "group_id"
	case conloomapi.ConvPrivate:
		action, idKey = "send_private_msg", "user_id"
	default:
		return "", nil, fmt.Errorf("napcat: unsupported conversation type %q", a.ConversationType)
	}
	target, err := strconv.ParseInt(a.PlatformConversationID, 10, 64)
	if err != nil {
		return "", nil, fmt.Errorf("napcat: invalid platform conversation id %q: %w", a.PlatformConversationID, err)
	}

	message := make([]map[string]any, 0, 2)
	if a.ReplyToPlatformMessageID != "" {
		message = append(message, map[string]any{"type": "reply", "data": map[string]any{"id": a.ReplyToPlatformMessageID}})
	}
	message = append(message, map[string]any{"type": "text", "data": map[string]any{"text": a.Content.Text}})

	return action, map[string]any{idKey: target, "message": message}, nil
}
