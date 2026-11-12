// Package router chooses a model profile by role for each request, based on
// context size, modality, privacy and per-role circuit breakers.
package router

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/provider/openai"
)

type Role string

const (
	RoleShort Role = "short_context"
	RoleLong  Role = "long_context"
)

const (
	defaultBreakerFailures = 3
	defaultBreakerCooldown = 30 * time.Second
)

var (
	ErrContextTooLarge = errors.New("router: context too large for every available profile")
	ErrNoProfile       = errors.New("router: no available profile")
)

type ChatClient interface {
	Chat(ctx context.Context, req openai.ChatRequest) (openai.ChatResponse, error)
}

type Profile struct {
	Role   Role
	Config config.ModelProfile
	Client ChatClient
}

// BreakerConfig sets how many consecutive failures open a role's breaker and
// how long it stays open. Zero values mean 3 failures and 30s.
type BreakerConfig struct {
	Failures int
	Cooldown time.Duration
}

type Request struct {
	NeedsImage            bool
	Sensitive             bool
	AllowLong             bool
	BaseTokens            int
	GroupMaxContextTokens int
	Exclude               []Role
}

type Decision struct {
	Profile         *Profile
	Reason          string
	PromptBudget    int
	MaxPromptTokens int
	MaxOutputTokens int
}

type breaker struct {
	failures int
	open     bool
	openedAt time.Time
}

type Router struct {
	profiles []*Profile
	bc       BreakerConfig
	now      func() time.Time

	mu       sync.Mutex
	breakers map[Role]*breaker
}

// EffectiveLimits returns the usable context window, the smallest of the
// native window, the configured window and groupCap (ignored when <= 0), and
// the largest prompt that leaves room for the reserved output and the safety
// margin.
func EffectiveLimits(c config.ContextLimits, groupCap int) (window, maxPrompt int) {
	window = min(c.NativeWindow, c.ConfiguredWindow)
	if groupCap > 0 {
		window = min(window, groupCap)
	}
	return window, window - c.ReservedOutputTokens - c.SafetyMarginTokens
}

// New returns a router over profiles. For each role the first profile with
// that role is used.
func New(profiles []*Profile, bc BreakerConfig, now func() time.Time) *Router {
	if bc.Failures <= 0 {
		bc.Failures = defaultBreakerFailures
	}
	if bc.Cooldown <= 0 {
		bc.Cooldown = defaultBreakerCooldown
	}
	return &Router{profiles: profiles, bc: bc, now: now, breakers: make(map[Role]*breaker)}
}

// Route prefers the short-context profile when the base prompt fits it and
// falls back to the long-context profile otherwise.
func (r *Router) Route(req Request) (Decision, error) {
	short := r.candidate(RoleShort, req)
	var long *Profile
	if req.AllowLong {
		long = r.candidate(RoleLong, req)
	}

	if short != nil {
		if d, ok := fit(short, req, "fits_short_context"); ok {
			return d, nil
		}
	}
	if long != nil {
		reason := "exceeds_short_fits_long"
		if short == nil {
			reason = "short_unavailable_fallback_long"
		}
		if d, ok := fit(long, req, reason); ok {
			return d, nil
		}
	}
	if short != nil || long != nil {
		return Decision{}, ErrContextTooLarge
	}
	return Decision{}, ErrNoProfile
}

func fit(p *Profile, req Request, reason string) (Decision, bool) {
	c := p.Config.Context
	_, maxPrompt := EffectiveLimits(c, req.GroupMaxContextTokens)
	if req.BaseTokens > maxPrompt {
		return Decision{}, false
	}
	return Decision{
		Profile:         p,
		Reason:          reason,
		PromptBudget:    max(min(c.SoftInputBudget, maxPrompt), req.BaseTokens),
		MaxPromptTokens: maxPrompt,
		MaxOutputTokens: c.ReservedOutputTokens,
	}, true
}

func (r *Router) candidate(role Role, req Request) *Profile {
	if slices.Contains(req.Exclude, role) || r.BreakerOpen(role) {
		return nil
	}
	i := slices.IndexFunc(r.profiles, func(p *Profile) bool { return p.Role == role })
	if i < 0 {
		return nil
	}
	p := r.profiles[i]
	if req.NeedsImage && !slices.Contains(p.Config.Modalities, "image") {
		return nil
	}
	if req.Sensitive && !p.Config.Privacy.AllowSensitiveContent {
		return nil
	}
	return p
}

// ReportSuccess closes the role's breaker and clears its failure count.
func (r *Router) ReportSuccess(role Role) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.breakers, role)
}

// ReportFailure records a failed call. Reaching the consecutive failure
// threshold opens the breaker; a failure after the cooldown (half-open)
// reopens it for another full cooldown.
func (r *Router) ReportFailure(role Role) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.breakers[role]
	if b == nil {
		b = &breaker{}
		r.breakers[role] = b
	}
	b.failures++
	if b.failures >= r.bc.Failures {
		b.open = true
		b.openedAt = r.now()
	}
}

// BreakerOpen reports whether the role's breaker is open and still cooling
// down. After the cooldown the role is routable again (half-open).
func (r *Router) BreakerOpen(role Role) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.breakers[role]
	return b != nil && b.open && r.now().Sub(b.openedAt) < r.bc.Cooldown
}
