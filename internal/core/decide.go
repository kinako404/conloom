// Package core implements ConLoom's message handling pipeline.
package core

import (
	"strings"
	"unicode"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const TriggerContinuation = conloomapi.TriggerContinuation

var KnownCommands = []string{"help", "stop", "persona", "pause", "resume", "主动", "proactive",
	"好感度", "affinity", "记忆", "memory", "记住", "remember", "忘记我", "forgetme"}

// ParseCommand reports whether text (after trimming) starts with prefix
// immediately followed by a known command word. cmd is lower-cased and args
// is the trimmed remainder after the command word.
func ParseCommand(text, prefix string) (cmd, args string, ok bool) {
	if prefix == "" {
		return "", "", false
	}
	text = strings.TrimSpace(text)
	rest, found := strings.CutPrefix(text, prefix)
	if !found {
		return "", "", false
	}
	word := rest
	if i := strings.IndexFunc(rest, unicode.IsSpace); i >= 0 {
		word, args = rest[:i], strings.TrimSpace(rest[i:])
	}
	word = strings.ToLower(word)
	for _, known := range KnownCommands {
		if word == known {
			return word, args, true
		}
	}
	return "", "", false
}

type DecideInput struct {
	BotInstanceID    string
	SelfPlatformID   string
	ConversationType string
	// Platform is the conversation's platform; private chats on
	// store.PlatformPanel are never denied.
	Platform       string
	PlatformUserID string
	Text           string
	CommandPrefix  string
	PrivateChat    string // "off", "admins", "allowlist" or "all"
	Mentions       []conloomapi.Mention
	Enabled        bool
	IsAdmin        bool
	// PrivateListed is set when the sender is listed as a private
	// conversation in the bot's enabled_conversations; only the "allowlist"
	// private chat mode uses it.
	PrivateListed bool
	ReplyToIsBot  bool
	// InContinuation is set when the sender takes part in the group's
	// ongoing session with the bot.
	InContinuation bool
	// MentionsOthers is set when the message mentions a user or everyone.
	MentionsOthers bool
	// ReplyToOther is set when the message replies to a stored message sent
	// by another member (neither the bot nor the sender).
	ReplyToOther bool
}

// Decision is the outcome of Decide. Reason is set only for ignore
// dispositions; Trigger, Command and Args only for reply. EndContinuation asks
// the caller to take the sender out of the session.
type Decision struct {
	Disposition     string
	Reason          string
	Trigger         string
	Command         string
	Args            string
	EndContinuation bool
}

// Decide classifies an incoming message as reply, observe or ignore.
func Decide(in DecideInput) Decision {
	if in.SelfPlatformID != "" && in.PlatformUserID == in.SelfPlatformID {
		return ignore(conloomapi.SuppressedSelfMessage)
	}
	private := in.ConversationType == conloomapi.ConvPrivate
	if !private && !in.Enabled {
		return ignore(conloomapi.SuppressedNotEnabled)
	}
	if private && privateDenied(in) {
		return ignore(conloomapi.SuppressedPrivateNotAllowed)
	}
	if cmd, args, ok := ParseCommand(in.Text, in.CommandPrefix); ok {
		return Decision{Disposition: conloomapi.DispositionReply, Trigger: conloomapi.TriggerCommand, Command: cmd, Args: args}
	}
	if private {
		return reply(conloomapi.TriggerPrivate)
	}
	for _, m := range in.Mentions {
		if m.Type == "bot" && m.ID == in.BotInstanceID {
			return reply(conloomapi.TriggerMention)
		}
	}
	if in.ReplyToIsBot {
		return reply(conloomapi.TriggerReply)
	}
	if in.InContinuation {
		if in.MentionsOthers || in.ReplyToOther {
			return Decision{Disposition: conloomapi.DispositionObserve, EndContinuation: true}
		}
		return reply(TriggerContinuation)
	}
	return Decision{Disposition: conloomapi.DispositionObserve}
}

func privateDenied(in DecideInput) bool {
	if in.Platform == store.PlatformPanel {
		return false
	}
	switch in.PrivateChat {
	case "off":
		return true
	case "admins":
		return !in.IsAdmin
	case "allowlist":
		return !in.IsAdmin && !in.PrivateListed
	}
	return false
}

func ignore(reason string) Decision {
	return Decision{Disposition: conloomapi.DispositionIgnore, Reason: reason}
}

func reply(trigger string) Decision {
	return Decision{Disposition: conloomapi.DispositionReply, Trigger: trigger}
}
