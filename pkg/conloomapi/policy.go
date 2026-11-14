package conloomapi

import (
	"bytes"
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
	Memory      MemoryPolicy      `json:"memory" yaml:"memory"`
}

// MemoryPolicy controls the group's memory: summaries, member notes and affinity.
// The zero value means the section is missing and is replaced by WithDefaults.
type MemoryPolicy struct {
	Enabled             bool     `json:"enabled" yaml:"enabled"`
	Affinity            bool     `json:"affinity" yaml:"affinity"`
	ConsolidateEvery    int      `json:"consolidate_every" yaml:"consolidate_every"`
	ConsolidateInterval Duration `json:"consolidate_interval" yaml:"consolidate_interval"`
	// ConsolidateMin is the fewest new messages consolidated because
	// ConsolidateInterval has passed.
	ConsolidateMin int `json:"consolidate_min" yaml:"consolidate_min"`
	Backlog        int `json:"backlog" yaml:"backlog"`
	// SummaryMaxChars and NoteMaxChars limit the summary and each member's
	// note, in characters; 0 means the default of the conversation's type.
	SummaryMaxChars int `json:"summary_max_chars" yaml:"summary_max_chars"`
	NoteMaxChars    int `json:"note_max_chars" yaml:"note_max_chars"`
}

// Bounds of a non-zero MemoryPolicy.SummaryMaxChars and NoteMaxChars.
const (
	MinSummaryMaxChars = 200
	MaxSummaryMaxChars = 8000
	MinNoteMaxChars    = 50
	MaxNoteMaxChars    = 2000
)

func DefaultMemoryPolicy() MemoryPolicy {
	return MemoryPolicy{
		Enabled:             false,
		Affinity:            true,
		ConsolidateEvery:    150,
		ConsolidateInterval: Duration{6 * time.Hour},
		ConsolidateMin:      20,
		Backlog:             1000,
	}
}

// UnmarshalJSON decodes over DefaultMemoryPolicy, so keys missing from the section
// take the built-in defaults while explicit zero values are kept.
func (m *MemoryPolicy) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == "null" {
		return nil
	}
	type plain MemoryPolicy
	v := plain(DefaultMemoryPolicy())
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*m = MemoryPolicy(v)
	return nil
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
	// The removed contact_cooldown key of stored sections is ignored.
	GroupHourlyLimit int                  `json:"group_hourly_limit" yaml:"group_hourly_limit"`
	DailyLimits      ProactiveDailyLimits `json:"daily_limits" yaml:"daily_limits"`
	// JoinLull is the pause after the last group message that triggers an
	// evaluation of whether to join the conversation.
	JoinLull Duration `json:"join_lull" yaml:"join_lull"`
	// JoinWindow is how far back the messages considered for joining reach.
	JoinWindow Duration `json:"join_window" yaml:"join_window"`
	// JoinMinMessages is the fewest non-bot messages in JoinWindow needed to join.
	JoinMinMessages int `json:"join_min_messages" yaml:"join_min_messages"`
	// JoinEvaluationsPerHour caps the join evaluations (model calls) per group per hour.
	JoinEvaluationsPerHour int `json:"join_evaluations_per_hour" yaml:"join_evaluations_per_hour"`
	// JoinCooldown is how long after the bot last spoke in the group before it may join.
	JoinCooldown Duration `json:"join_cooldown" yaml:"join_cooldown"`
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

// Reply quote modes: QuoteAuto quotes the trigger message only when the reply
// could be mistaken for an answer to something else.
const (
	QuoteAuto   = "auto"
	QuoteAlways = "always"
	QuoteNever  = "never"
)

type ConcurrencyPolicy struct {
	MaxInteractiveRequests int    `json:"max_interactive_requests" yaml:"max_interactive_requests"`
	MaxQueuedRequests      int    `json:"max_queued_requests" yaml:"max_queued_requests"`
	Priority               string `json:"priority" yaml:"priority"`
	AllowLongRequests      bool   `json:"allow_long_requests" yaml:"allow_long_requests"`
}

type ReplyPolicy struct {
	CommandPrefix string `json:"command_prefix" yaml:"command_prefix"`
	// ContinuationWindow is how long a conversation with the bot stays open
	// after its last activity; 0 disables it, so only direct triggers get replies.
	ContinuationWindow     Duration `json:"continuation_window" yaml:"continuation_window"`
	Debounce               Duration `json:"debounce" yaml:"debounce"`
	MinReplyInterval       Duration `json:"min_reply_interval" yaml:"min_reply_interval"`
	UserRateLimitPerMinute int      `json:"user_rate_limit_per_minute" yaml:"user_rate_limit_per_minute"`
	// TurnGap is the pause that ends a participant's turn; TurnMaxWait caps
	// how long a turn may keep growing.
	TurnGap     Duration `json:"turn_gap" yaml:"turn_gap"`
	TurnMaxWait Duration `json:"turn_max_wait" yaml:"turn_max_wait"`
	// TurnCooldown is how long after the bot last spoke in the group a turn
	// nobody addressed to the bot waits before it is answered.
	TurnCooldown Duration `json:"turn_cooldown" yaml:"turn_cooldown"`
	// MaxUnpromptedReplies ends a conversation after this many replies since
	// the bot was last addressed directly.
	MaxUnpromptedReplies int `json:"max_unprompted_replies" yaml:"max_unprompted_replies"`
	// MaxConsecutiveSkips ends a conversation after this many turns in a row
	// that the model chose not to answer.
	MaxConsecutiveSkips int `json:"max_consecutive_skips" yaml:"max_consecutive_skips"`
	// Quote is when group replies quote the message they answer: auto, always or never.
	Quote string `json:"quote" yaml:"quote"`
	// MaxParts is how many separate messages one reply may be split into.
	MaxParts int `json:"max_parts" yaml:"max_parts"`
	// PrivateWait is the pause in a private chat that ends the user's turn;
	// PrivateMaxWait caps how long the turn may keep growing.
	PrivateWait    Duration `json:"private_wait" yaml:"private_wait"`
	PrivateMaxWait Duration `json:"private_max_wait" yaml:"private_max_wait"`
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

// PolicyPut replaces a group policy. Its proactive and memory sections may each be left out to keep
// the group's current one: a section counts as omitted when it is zero and its Present flag is false.
// UnmarshalJSON sets ProactivePresent and MemoryPresent when the body has a non-null policy.proactive
// or policy.memory key, so a JSON section such as {"enabled":false} is applied as given (missing
// memory keys take the built-in defaults) and an invalid one fails validation. Go callers that build
// a PolicyPut leave the flags false, and a zero section from them is omitted.
type PolicyPut struct {
	Policy GroupPolicy `json:"policy" yaml:"policy"`
	Reason string      `json:"reason" yaml:"reason"`

	ProactivePresent bool `json:"-" yaml:"-"`
	MemoryPresent    bool `json:"-" yaml:"-"`
}

// ProactiveOmitted reports whether the update leaves the proactive section unchanged.
func (p PolicyPut) ProactiveOmitted() bool {
	return !p.ProactivePresent && p.Policy.Proactive == (ProactivePolicy{})
}

// MemoryOmitted reports whether the update leaves the memory section unchanged.
func (p PolicyPut) MemoryOmitted() bool {
	return !p.MemoryPresent && p.Policy.Memory == (MemoryPolicy{})
}

// groupPolicyFields has GroupPolicy's fields; policyPutWire's own Proactive and Memory fields shadow the embedded ones.
type groupPolicyFields GroupPolicy

type policyPutWire struct {
	Policy struct {
		groupPolicyFields
		Proactive *ProactivePolicy `json:"proactive,omitempty"`
		Memory    *MemoryPolicy    `json:"memory,omitempty"`
	} `json:"policy"`
	Reason string `json:"reason"`
}

// MarshalJSON writes the PolicyPut wire format and leaves out omitted proactive and memory sections.
func (p PolicyPut) MarshalJSON() ([]byte, error) {
	var w policyPutWire
	w.Policy.groupPolicyFields = groupPolicyFields(p.Policy)
	if !p.ProactiveOmitted() {
		w.Policy.Proactive = &p.Policy.Proactive
	}
	if !p.MemoryOmitted() {
		w.Policy.Memory = &p.Policy.Memory
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
	if w.Policy.Memory != nil {
		p.Policy.Memory = *w.Policy.Memory
		p.MemoryPresent = true
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
			TurnGap:                Duration{6 * time.Second},
			TurnMaxWait:            Duration{20 * time.Second},
			TurnCooldown:           Duration{45 * time.Second},
			MaxUnpromptedReplies:   4,
			MaxConsecutiveSkips:    2,
			Quote:                  QuoteAuto,
			MaxParts:               1,
			PrivateWait:            Duration{6 * time.Second},
			PrivateMaxWait:         Duration{20 * time.Second},
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
			Enabled:                true,
			DefaultLevel:           ProactiveLow,
			EligibilityTTL:         Duration{720 * time.Hour},
			UserCooldown:           Duration{30 * time.Minute},
			GroupHourlyLimit:       4,
			DailyLimits:            ProactiveDailyLimits{Low: 2, Mid: 4, High: 8},
			JoinLull:               Duration{15 * time.Second},
			JoinWindow:             Duration{5 * time.Minute},
			JoinMinMessages:        4,
			JoinEvaluationsPerHour: 4,
			JoinCooldown:           Duration{10 * time.Minute},
		},
		Memory: DefaultMemoryPolicy(),
	}
}

// WithDefaults returns p with a missing (zero) Proactive or Memory section replaced by def's,
// and each turn-taking, quote, max_parts and join setting that is zero (missing from policies
// stored before it existed; zero is never valid for them) taken from def. Zero is a valid
// private_wait, so private_wait and private_max_wait are taken from def only when both are zero.
// Memory's summary_max_chars and note_max_chars are never taken from def: their zero means the
// default of the conversation's type.
func (p GroupPolicy) WithDefaults(def GroupPolicy) GroupPolicy {
	if p.Proactive == (ProactivePolicy{}) {
		p.Proactive = def.Proactive
	}
	if p.Memory == (MemoryPolicy{}) {
		p.Memory = def.Memory
	}
	fillZero(&p.Reply.TurnGap, def.Reply.TurnGap)
	fillZero(&p.Reply.TurnMaxWait, def.Reply.TurnMaxWait)
	fillZero(&p.Reply.TurnCooldown, def.Reply.TurnCooldown)
	fillZero(&p.Reply.MaxUnpromptedReplies, def.Reply.MaxUnpromptedReplies)
	fillZero(&p.Reply.MaxConsecutiveSkips, def.Reply.MaxConsecutiveSkips)
	fillZero(&p.Reply.Quote, def.Reply.Quote)
	fillZero(&p.Reply.MaxParts, def.Reply.MaxParts)
	if p.Reply.PrivateWait == (Duration{}) && p.Reply.PrivateMaxWait == (Duration{}) {
		p.Reply.PrivateWait = def.Reply.PrivateWait
		p.Reply.PrivateMaxWait = def.Reply.PrivateMaxWait
	}
	fillZero(&p.Proactive.JoinLull, def.Proactive.JoinLull)
	fillZero(&p.Proactive.JoinWindow, def.Proactive.JoinWindow)
	fillZero(&p.Proactive.JoinMinMessages, def.Proactive.JoinMinMessages)
	fillZero(&p.Proactive.JoinEvaluationsPerHour, def.Proactive.JoinEvaluationsPerHour)
	fillZero(&p.Proactive.JoinCooldown, def.Proactive.JoinCooldown)
	return p
}

func fillZero[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
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
	if r.ContinuationWindow.Duration < 0 || r.ContinuationWindow.Duration > 10*time.Minute {
		fail("reply.continuation_window must be in [0, 10m], got %s", r.ContinuationWindow)
	}
	for _, d := range []struct {
		path string
		v    Duration
	}{
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
	if r.TurnGap.Duration < time.Second || r.TurnGap.Duration > time.Minute {
		fail("reply.turn_gap must be in [1s, 60s], got %s", r.TurnGap)
	}
	if r.TurnMaxWait.Duration < r.TurnGap.Duration || r.TurnMaxWait.Duration > 2*time.Minute {
		fail("reply.turn_max_wait must be in [reply.turn_gap (%s), 2m], got %s", r.TurnGap, r.TurnMaxWait)
	}
	if r.TurnCooldown.Duration < time.Second || r.TurnCooldown.Duration > 10*time.Minute {
		fail("reply.turn_cooldown must be in [1s, 10m], got %s", r.TurnCooldown)
	}
	if w := r.ContinuationWindow.Duration; w != 0 && w <= r.TurnCooldown.Duration {
		fail("reply.continuation_window must be 0 or longer than reply.turn_cooldown (%s), got %s", r.TurnCooldown, r.ContinuationWindow)
	}
	if r.MaxUnpromptedReplies < 1 || r.MaxUnpromptedReplies > 20 {
		fail("reply.max_unprompted_replies must be in [1, 20], got %d", r.MaxUnpromptedReplies)
	}
	if r.MaxConsecutiveSkips < 1 || r.MaxConsecutiveSkips > 10 {
		fail("reply.max_consecutive_skips must be in [1, 10], got %d", r.MaxConsecutiveSkips)
	}
	if !slices.Contains([]string{QuoteAuto, QuoteAlways, QuoteNever}, r.Quote) {
		fail("reply.quote must be one of auto|always|never, got %q", r.Quote)
	}
	if r.MaxParts < 1 || r.MaxParts > 5 {
		fail("reply.max_parts must be in [1, 5], got %d", r.MaxParts)
	}
	if r.PrivateWait.Duration < 0 || r.PrivateWait.Duration > time.Minute {
		fail("reply.private_wait must be in [0, 60s], got %s", r.PrivateWait)
	}
	if r.PrivateMaxWait.Duration < r.PrivateWait.Duration || r.PrivateMaxWait.Duration > 2*time.Minute {
		fail("reply.private_max_wait must be in [reply.private_wait (%s), 2m], got %s", r.PrivateWait, r.PrivateMaxWait)
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
	for _, d := range []struct {
		path     string
		v        Duration
		min, max time.Duration
		bounds   string
	}{
		{"proactive.join_lull", a.JoinLull, 5 * time.Second, 5 * time.Minute, "[5s, 5m]"},
		{"proactive.join_window", a.JoinWindow, time.Minute, 30 * time.Minute, "[1m, 30m]"},
		{"proactive.join_cooldown", a.JoinCooldown, time.Minute, 24 * time.Hour, "[1m, 24h]"},
	} {
		if d.v.Duration < d.min || d.v.Duration > d.max {
			fail("%s must be in %s, got %s", d.path, d.bounds, d.v)
		}
	}
	if a.JoinMinMessages < 2 || a.JoinMinMessages > 50 {
		fail("proactive.join_min_messages must be in [2, 50], got %d", a.JoinMinMessages)
	}
	if a.JoinEvaluationsPerHour < 1 || a.JoinEvaluationsPerHour > 60 {
		fail("proactive.join_evaluations_per_hour must be in [1, 60], got %d", a.JoinEvaluationsPerHour)
	}
	m := p.Memory
	if m.ConsolidateEvery < 30 || m.ConsolidateEvery > 2000 {
		fail("memory.consolidate_every must be in [30, 2000], got %d", m.ConsolidateEvery)
	}
	if m.ConsolidateInterval.Duration < 30*time.Minute || m.ConsolidateInterval.Duration > 48*time.Hour {
		fail("memory.consolidate_interval must be in [30m, 48h], got %s", m.ConsolidateInterval)
	}
	if m.ConsolidateMin < 1 || m.ConsolidateMin > 2000 {
		fail("memory.consolidate_min must be in [1, 2000], got %d", m.ConsolidateMin)
	}
	if m.Backlog < 0 || m.Backlog > 5000 {
		fail("memory.backlog must be in [0, 5000], got %d", m.Backlog)
	}
	if v := m.SummaryMaxChars; v != 0 && (v < MinSummaryMaxChars || v > MaxSummaryMaxChars) {
		fail("memory.summary_max_chars must be 0 or in [%d, %d], got %d", MinSummaryMaxChars, MaxSummaryMaxChars, v)
	}
	if v := m.NoteMaxChars; v != 0 && (v < MinNoteMaxChars || v > MaxNoteMaxChars) {
		fail("memory.note_max_chars must be 0 or in [%d, %d], got %d", MinNoteMaxChars, MaxNoteMaxChars, v)
	}
	if m.Enabled && !c.AllowLongRequests {
		fail("memory.enabled requires concurrency.allow_long_requests")
	}
	return errors.Join(errs...)
}
