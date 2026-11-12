package conloomapi

import (
	"encoding/json"
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
	Proactive   ProactivePolicy   `json:"proactive" yaml:"proactive"`
}

const (
	ProactiveOff  = "off"
	ProactiveLow  = "low"
	ProactiveMid  = "mid"
	ProactiveHigh = "high"
)

// ProactivePolicy controls unprompted replies to members who have engaged the bot.
// The zero value means the section is missing and is replaced by WithDefaults.
type ProactivePolicy struct {
	Enabled        bool     `json:"enabled" yaml:"enabled"`
	DefaultLevel   string   `json:"default_level" yaml:"default_level"`
	EligibilityTTL Duration `json:"eligibility_ttl" yaml:"eligibility_ttl"`
	UserCooldown   Duration `json:"user_cooldown" yaml:"user_cooldown"`
	// ContactCooldown is how long after the bot last replied to a member, or
	// their continuation ended, before it may speak up to them; 0 (the
	// default, also for stored sections written before the field existed)
	// means no wait.
	ContactCooldown  Duration             `json:"contact_cooldown" yaml:"contact_cooldown"`
	GroupHourlyLimit int                  `json:"group_hourly_limit" yaml:"group_hourly_limit"`
	DailyLimits      ProactiveDailyLimits `json:"daily_limits" yaml:"daily_limits"`
}

type ProactiveDailyLimits struct {
	Low  int `json:"low" yaml:"low"`
	Mid  int `json:"mid" yaml:"mid"`
	High int `json:"high" yaml:"high"`
}

// DailyLimit returns the per-user daily proactive reply limit for level; off and unknown levels get 0.
func (p ProactivePolicy) DailyLimit(level string) int {
	switch level {
	case ProactiveLow:
		return p.DailyLimits.Low
	case ProactiveMid:
		return p.DailyLimits.Mid
	case ProactiveHigh:
		return p.DailyLimits.High
	}
	return 0
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

// PolicyPut replaces a group policy. Its proactive section may be left out to keep the group's
// current one: the section counts as omitted when it is zero and ProactivePresent is false.
// UnmarshalJSON sets ProactivePresent when the body has a non-null policy.proactive key, so a
// JSON section such as {"enabled":false} is applied as given and fails validation. Go callers that
// build a PolicyPut leave ProactivePresent false, and a zero section from them is omitted.
type PolicyPut struct {
	Policy GroupPolicy `json:"policy" yaml:"policy"`
	Reason string      `json:"reason" yaml:"reason"`

	ProactivePresent bool `json:"-" yaml:"-"`
}

// ProactiveOmitted reports whether the update leaves the proactive section unchanged.
func (p PolicyPut) ProactiveOmitted() bool {
	return !p.ProactivePresent && p.Policy.Proactive == (ProactivePolicy{})
}

// groupPolicyFields has GroupPolicy's fields; policyPutWire's own Proactive field shadows the embedded one.
type groupPolicyFields GroupPolicy

type policyPutWire struct {
	Policy struct {
		groupPolicyFields
		Proactive *ProactivePolicy `json:"proactive,omitempty"`
	} `json:"policy"`
	Reason string `json:"reason"`
}

// MarshalJSON writes the PolicyPut wire format and leaves out an omitted proactive section.
func (p PolicyPut) MarshalJSON() ([]byte, error) {
	var w policyPutWire
	w.Policy.groupPolicyFields = groupPolicyFields(p.Policy)
	if !p.ProactiveOmitted() {
		w.Policy.Proactive = &p.Policy.Proactive
	}
	w.Reason = p.Reason
	return json.Marshal(w)
}

func (p *PolicyPut) UnmarshalJSON(b []byte) error {
	var w policyPutWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*p = PolicyPut{Policy: GroupPolicy(w.Policy.groupPolicyFields), Reason: w.Reason}
	if w.Policy.Proactive != nil {
		p.Policy.Proactive = *w.Policy.Proactive
		p.ProactivePresent = true
	}
	return nil
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
		Proactive: ProactivePolicy{
			Enabled:          true,
			DefaultLevel:     ProactiveLow,
			EligibilityTTL:   Duration{720 * time.Hour},
			UserCooldown:     Duration{30 * time.Minute},
			ContactCooldown:  Duration{0},
			GroupHourlyLimit: 4,
			DailyLimits:      ProactiveDailyLimits{Low: 2, Mid: 4, High: 8},
		},
	}
}

// WithDefaults returns p with a missing (zero) Proactive section replaced by def.Proactive.
func (p GroupPolicy) WithDefaults(def GroupPolicy) GroupPolicy {
	if p.Proactive == (ProactivePolicy{}) {
		p.Proactive = def.Proactive
	}
	return p
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
	a := p.Proactive
	if !slices.Contains([]string{ProactiveOff, ProactiveLow, ProactiveMid, ProactiveHigh}, a.DefaultLevel) {
		fail("proactive.default_level must be one of off|low|mid|high, got %q", a.DefaultLevel)
	}
	if a.EligibilityTTL.Duration < time.Hour || a.EligibilityTTL.Duration > 8760*time.Hour {
		fail("proactive.eligibility_ttl must be in [1h, 8760h], got %s", a.EligibilityTTL)
	}
	if a.UserCooldown.Duration < 0 || a.UserCooldown.Duration > 24*time.Hour {
		fail("proactive.user_cooldown must be in [0, 24h], got %s", a.UserCooldown)
	}
	if a.ContactCooldown.Duration < 0 || a.ContactCooldown.Duration > 24*time.Hour {
		fail("proactive.contact_cooldown must be in [0, 24h], got %s", a.ContactCooldown)
	}
	if a.GroupHourlyLimit < 0 || a.GroupHourlyLimit > 60 {
		fail("proactive.group_hourly_limit must be in [0, 60], got %d", a.GroupHourlyLimit)
	}
	dl := a.DailyLimits
	for _, l := range []struct {
		path string
		v    int
	}{
		{"proactive.daily_limits.low", dl.Low},
		{"proactive.daily_limits.mid", dl.Mid},
		{"proactive.daily_limits.high", dl.High},
	} {
		if l.v < 0 || l.v > 100 {
			fail("%s must be in [0, 100], got %d", l.path, l.v)
		}
	}
	if dl.Low > dl.Mid || dl.Mid > dl.High {
		fail("proactive.daily_limits must satisfy low <= mid <= high, got %d/%d/%d", dl.Low, dl.Mid, dl.High)
	}
	return errors.Join(errs...)
}
