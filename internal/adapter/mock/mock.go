// Package mock is a scriptable adapter that talks to ConLoom through the public
// client, for local testing without a chat platform.
package mock

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// maxPollWait is the longest wait the server honours for one outbox poll.
const maxPollWait = 30 * time.Second

// Conv is a conversation by its platform type ("group" or "private") and ID.
type Conv struct{ Type, PlatformID string }

// User is a platform user.
type User struct{ PlatformID, Name string }

// SayOptions are optional parts of a message. ReplyTo is a standard message ID.
type SayOptions struct {
	MentionBot bool
	ReplyTo    string
	Images     int
}

// Mock sends messages as platform users and delivers the bot's actions.
// Standard IDs follow the same scheme as a real adapter: conversations are
// group_<id> or private_<id>, actors user_<id> and messages msg_<platform ID>.
type Mock struct {
	c        *client.Client
	botID    string
	platform string
	msgSeq   atomic.Int64
	botSeq   atomic.Int64
}

func New(c *client.Client, botID, platform string) *Mock {
	return &Mock{c: c, botID: botID, platform: platform}
}

// Say submits text from user in conv. Platform message IDs count up from 1
// (RunREPL starts them higher); the idempotency key is mock:<botID>:<n>. It
// returns the server's response and the standard message ID.
func (m *Mock) Say(ctx context.Context, conv Conv, user User, text string, o SayOptions) (conloomapi.EventAccepted, string, error) {
	pmid := strconv.FormatInt(m.msgSeq.Add(1), 10)
	msg := conloomapi.Message{
		ID:                "msg_" + pmid,
		PlatformMessageID: pmid,
		Text:              text,
		Mentions:          []conloomapi.Mention{},
		Attachments:       []conloomapi.Attachment{},
	}
	if o.MentionBot {
		msg.Mentions = append(msg.Mentions, conloomapi.Mention{Type: "bot", ID: m.botID})
	}
	if o.ReplyTo != "" {
		replyTo := o.ReplyTo
		msg.ReplyTo = &replyTo
	}
	for i := range o.Images {
		msg.Attachments = append(msg.Attachments, conloomapi.Attachment{Type: "image", Name: fmt.Sprintf("mock_%s_%d.png", pmid, i+1)})
	}

	trigger := conloomapi.TriggerObserve
	switch {
	case conv.Type == conloomapi.ConvPrivate:
		trigger = conloomapi.TriggerPrivate
	case o.MentionBot:
		trigger = conloomapi.TriggerMention
	case o.ReplyTo != "":
		trigger = conloomapi.TriggerReply
	case strings.HasPrefix(strings.TrimSpace(text), "/"):
		trigger = conloomapi.TriggerCommand
	}

	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_" + pmid,
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: m.botID,
		Platform:      m.platform,
		OccurredAt:    time.Now().UTC(),
		Conversation: conloomapi.Conversation{
			Type: conv.Type, ID: conv.Type + "_" + conv.PlatformID, PlatformConversationID: conv.PlatformID,
		},
		Actor: conloomapi.Actor{
			ID: "user_" + user.PlatformID, PlatformUserID: user.PlatformID, DisplayName: user.Name, Roles: []string{"member"},
		},
		Trigger: conloomapi.Trigger{Type: trigger, RequestedResponse: trigger != conloomapi.TriggerObserve},
		Message: msg,
	}
	resp, err := m.c.SubmitEvent(ctx, "mock:"+m.botID+":"+pmid, ev)
	return resp, msg.ID, err
}

// Drain polls the outbox until at least one action arrives or wait elapses,
// acknowledges every action it received as sent with platform message ID
// mockbot-<n> (counting up from 1 unless RunREPL started it higher), and
// returns them.
func (m *Mock) Drain(ctx context.Context, wait time.Duration) ([]conloomapi.Action, error) {
	deadline := time.Now().Add(wait)
	for {
		remaining := max(time.Until(deadline), 0)
		actions, err := m.c.PollOutbox(ctx, 0, min(remaining, maxPollWait))
		if err != nil {
			return nil, err
		}
		if len(actions) > 0 {
			return actions, m.ack(ctx, actions)
		}
		if remaining == 0 || !time.Now().Before(deadline) {
			return actions, nil
		}
	}
}

func (m *Mock) ack(ctx context.Context, actions []conloomapi.Action) error {
	for _, a := range actions {
		req := conloomapi.AckRequest{
			Status:            conloomapi.AckSent,
			PlatformMessageID: "mockbot-" + strconv.FormatInt(m.botSeq.Add(1), 10),
		}
		if err := m.c.Ack(ctx, a.ActionID, req); err != nil {
			return fmt.Errorf("mock: ack %s: %w", a.ActionID, err)
		}
	}
	return nil
}
