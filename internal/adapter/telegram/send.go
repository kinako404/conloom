package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// maxMessageLen is Telegram's limit on a message's text, in UTF-16 code units.
const maxMessageLen = 4096

var errUnsupportedAction = errors.New("telegram: unsupported action type")

func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// splitText splits text into parts of at most limit UTF-16 code units. A cut
// falls on the last blank line, else the last line break, in the second half
// of a full part (the break itself is dropped), else at the limit; a
// character is never split.
func splitText(text string, limit int) []string {
	var parts []string
	for {
		cut, units := len(text), 0
		for i, r := range text {
			if units+utf16Len(r) > limit {
				cut = i
				break
			}
			units += utf16Len(r)
		}
		if cut == len(text) {
			return append(parts, text)
		}
		at, skip := cut, 0
		if i := strings.LastIndex(text[:cut], "\n\n"); i > 0 && i >= cut/2 {
			at, skip = i, 2
		} else if i := strings.LastIndex(text[:cut], "\n"); i > 0 && i >= cut/2 {
			at, skip = i, 1
		}
		parts = append(parts, text[:at])
		text = text[at+skip:]
	}
}

// sendPlan is an action as sendMessage calls: its parts go to chatID, and
// the first quotes message replyTo when it is not zero.
type sendPlan struct {
	chatID  int64
	replyTo int64
	parts   []string
}

// buildSend plans the sendMessage calls of a message.send action. The quoted
// message's platform ID has the form <chat_id>_<message_id>; a quote of
// another chat is dropped.
func buildSend(a conloomapi.Action) (sendPlan, error) {
	if a.Type != conloomapi.ActionMessageSend {
		return sendPlan{}, fmt.Errorf("%w: %q", errUnsupportedAction, a.Type)
	}
	chatID, err := strconv.ParseInt(a.PlatformConversationID, 10, 64)
	if err != nil {
		return sendPlan{}, fmt.Errorf("telegram: invalid platform conversation id %q", a.PlatformConversationID)
	}
	p := sendPlan{chatID: chatID}
	for _, part := range splitText(a.Content.Text, maxMessageLen) {
		if strings.TrimSpace(part) != "" {
			p.parts = append(p.parts, part)
		}
	}
	if len(p.parts) == 0 {
		return sendPlan{}, errors.New("telegram: the message text is empty")
	}
	if i := strings.LastIndexByte(a.ReplyToPlatformMessageID, '_'); i > 0 && a.ReplyToPlatformMessageID[:i] == a.PlatformConversationID {
		if id, err := strconv.ParseInt(a.ReplyToPlatformMessageID[i+1:], 10, 64); err == nil {
			p.replyTo = id
		}
	}
	return p, nil
}

// deliver sends one action unless the dedup log shows it was already sent,
// then ACKs it, like the NapCat adapter. If the first part never reached
// Telegram the action is left un-ACKed for redelivery and the error is
// returned. If Telegram refused it, or the outcome is unknown, the action is
// recorded with an empty platform message ID and ACKed failed, so it is never
// sent again. Once the first part is sent the action is recorded with that
// part's ID; a later part that fails is dropped rather than risking a resend.
func (a *Adapter) deliver(ctx context.Context, act conloomapi.Action) error {
	log := a.log.With("action_id", act.ActionID, "conversation_id", act.ConversationID)
	if pmid, ok := a.dedup.Seen(act.ActionID); ok {
		if pmid == "" {
			log.Warn("action may already have been sent; acknowledging it as failed without resending")
			a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckFailed, Error: outcomeUnknown})
			return nil
		}
		log.Info("action already sent; acknowledging again", "platform_message_id", pmid)
		a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: pmid})
		return nil
	}
	plan, err := buildSend(act)
	if errors.Is(err, errUnsupportedAction) {
		log.Info("unsupported action", "type", act.Type)
		a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckUnsupported, Error: err.Error()})
		return nil
	}
	if err != nil {
		log.Error("cannot build the Telegram request", "error", err)
		a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckFailed, Error: err.Error()})
		return nil
	}

	var first string
	for i, part := range plan.parts {
		params := sendMessageParams{ChatID: plan.chatID, Text: part}
		if i == 0 && plan.replyTo != 0 {
			params.ReplyParameters = &replyParameters{MessageID: plan.replyTo, AllowSendingWithoutReply: true}
		}
		msg, err := a.sendMessage(ctx, log, params)
		if i == 0 {
			a.typing.stop(plan.chatID)
		}
		if err == nil {
			if i == 0 {
				chatID := msg.Chat.ID
				if chatID == 0 {
					chatID = plan.chatID
				}
				if msg.MessageID == 0 {
					log.Warn("send response has no message_id")
				} else {
					first = platformMessageID(chatID, msg.MessageID)
				}
				if err := a.dedup.Mark(act.ActionID, first); err != nil {
					log.Error("recording the sent action failed", "error", err)
				}
			}
			continue
		}
		if i > 0 {
			log.Warn("reply only partly delivered; dropping the remaining parts", "parts_sent", i, "parts", len(plan.parts), "error", err)
			break
		}
		if errors.Is(err, errNotSent) {
			if ctx.Err() != nil {
				return nil
			}
			log.Warn("sending to Telegram failed; leaving the action for redelivery", "error", err)
			return err
		}
		reason := err.Error()
		if ae := (*apiError)(nil); errors.As(err, &ae) && ae.Status < 500 {
			log.Error("Telegram rejected the message", "error", err, obs.MessageAttr(a.logCfg, act.Content.Text))
		} else {
			reason = outcomeUnknown + ": " + reason
			log.Error("message delivery outcome unknown; it will not be sent again", "error", err,
				obs.MessageAttr(a.logCfg, act.Content.Text))
		}
		if err := a.dedup.Mark(act.ActionID, ""); err != nil {
			log.Error("recording the action failed", "error", err)
		}
		a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckFailed, Error: reason})
		return nil
	}
	log.Info("message sent", "platform_message_id", first, "parts", len(plan.parts), obs.MessageAttr(a.logCfg, act.Content.Text))
	a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: first})
	return nil
}

// sendMessage sends one message under its own timeout, which ctx ending does
// not cut short. On flood control (429) it waits retry_after and tries again,
// up to maxRateLimitRetries times; if ctx ends while waiting, the message was
// not sent.
func (a *Adapter) sendMessage(ctx context.Context, log *slog.Logger, params sendMessageParams) (message, error) {
	for attempt := 0; ; attempt++ {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.sendTimeout)
		var m message
		err := a.api.call(sctx, "sendMessage", params, &m)
		cancel()
		ae := (*apiError)(nil)
		if !errors.As(err, &ae) || ae.RetryAfter <= 0 || attempt >= a.maxRateLimitRetries {
			return m, err
		}
		log.Warn("Telegram flood control; waiting before retrying", "retry_after", ae.RetryAfter.String())
		if !sleep(ctx, ae.RetryAfter) {
			return message{}, fmt.Errorf("telegram: sendMessage: %w: stopped while waiting for flood control", errNotSent)
		}
	}
}
