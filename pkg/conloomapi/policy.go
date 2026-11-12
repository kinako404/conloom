package conloomapi

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

type GroupPolicy struct {
	Concurrency ConcurrencyPolicy `json:"concurrency" yaml:"concurrency"`
	Reply       ReplyPolicy       `json:"reply" yaml:"reply"`
	Context     ContextPolicy     `json:"context" yaml:"context"`
	Review      ReviewPolicy      `json:"review" yaml:"review"`
}

type ConcurrencyPolicy struct {
	MaxInteractiveRequests int    `json:"max_interactive_requests" yaml:"max_interactive_requests"`
	MaxQueuedRequests      int    `json:"max_queued_requests" yaml:"max_queued_requests"`
	Priority               string `json:"priority" yaml:"priority"`
	AllowLongRequests      bool   `json:"allow_long_requests" yaml:"allow_long_requests"`
}

type ReplyPolicy struct {
	CommandPrefix          string   `json:"command_prefix" yaml:"command_prefix"`
	ContinuationWindow     Duration `json:"continuation_window" yaml:"continuation_window"`
	Debounce               Duration `json:"debounce" yaml:"debounce"`
	MinReplyInterval       Duration `json:"min_reply_interval" yaml:"min_reply_interval"`
	UserRateLimitPerMinute int      `json:"user_rate_limit_per_minute" yaml:"user_rate_limit_per_minute"`
}

type ContextPolicy struct {
	MaxRecentMessages int `json:"max_recent_messages" yaml:"max_recent_messages"`
	MaxContextTokens  int `json:"max_context_tokens" yaml:"max_context_tokens"`
}

type ReviewPolicy struct {
	Level        string   `json:"level" yaml:"level"`
	ReviewInput  bool     `json:"review_input" yaml:"review_input"`
	ReviewOutput bool     `json:"review_output" yaml:"review_output"`
	Action       string   `json:"action" yaml:"action"`
	BlockedWords []string `json:"blocked_words,omitempty" yaml:"blocked_words,omitempty"`
}

type PolicyState struct {
	ConversationID string      `json:"conversation_id" yaml:"conversation_id"`
	Version        int         `json:"version" yaml:"version"`
	Policy         GroupPolicy `json:"policy" yaml:"policy"`
	Reason         string      `json:"reason" yaml:"reason"`
	UpdatedBy      string      `json:"updated_by" yaml:"updated_by"`
	CreatedAt      time.Time   `json:"created_at" yaml:"created_at"`
}

type PolicyPut struct {
	Policy GroupPolicy `json:"policy" yaml:"policy"`
	Reason string      `json:"reason" yaml:"reason"`
}

func DefaultGroupPolicy() GroupPolicy {
	return GroupPolicy{
		Concurrency: ConcurrencyPolicy{
			MaxInteractiveRequests: 1,
			MaxQueuedRequests:      10,
			Priority:               "normal",
			AllowLongRequests:      true,
		},
		Reply: ReplyPolicy{
			CommandPrefix:          "/",
			ContinuationWindow:     Duration{0},
			Debounce:               Duration{1500 * time.Millisecond},
			MinReplyInterval:       Duration{2 * time.Second},
			UserRateLimitPerMinute: 6,
		},
		Context: ContextPolicy{
			MaxRecentMessages: 50,
			MaxContextTokens:  0,
		},
		Review: ReviewPolicy{
			Level:        "basic",
			ReviewInput:  true,
			ReviewOutput: true,
			Action:       "block",
		},
	}
}

// Validate reports every rule violation, each prefixed with its snake_case field path.
func (p GroupPolicy) Validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	c := p.Concurrency
	if c.MaxInteractiveRequests < 0 || c.MaxInteractiveRequests > 2 {
		fail("concurrency.max_interactive_requests must be in 0..2, got %d", c.MaxInteractiveRequests)
	}
	if c.MaxQueuedRequests < 1 {
		fail("concurrency.max_queued_requests must be >= 1, got %d", c.MaxQueuedRequests)
	}
	if !slices.Contains([]string{"low", "normal", "high"}, c.Priority) {
		fail("concurrency.priority must be one of low|normal|high, got %q", c.Priority)
	}
	r := p.Reply
	for _, d := range []struct {
		path string
		v    Duration
	}{
		{"reply.continuation_window", r.ContinuationWindow},
		{"reply.debounce", r.Debounce},
		{"reply.min_reply_interval", r.MinReplyInterval},
	} {
		if d.v.Duration < 0 || d.v.Duration > time.Minute {
			fail("%s must be in [0, 1m], got %s", d.path, d.v)
		}
	}
	if r.UserRateLimitPerMinute < 1 {
		fail("reply.user_rate_limit_per_minute must be >= 1, got %d", r.UserRateLimitPerMinute)
	}
	if p.Context.MaxRecentMessages < 0 || p.Context.MaxRecentMessages > 500 {
		fail("context.max_recent_messages must be in [0, 500], got %d", p.Context.MaxRecentMessages)
	}
	if p.Context.MaxContextTokens < 0 {
		fail("context.max_context_tokens must be >= 0, got %d", p.Context.MaxContextTokens)
	}
	if !slices.Contains([]string{"basic", "standard", "strict"}, p.Review.Level) {
		fail("review.level must be one of basic|standard|strict, got %q", p.Review.Level)
	}
	if !slices.Contains([]string{"allow", "sanitize", "block"}, p.Review.Action) {
		fail("review.action must be one of allow|sanitize|block, got %q", p.Review.Action)
	}
	return errors.Join(errs...)
}
