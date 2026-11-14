// Package policy manages versioned per-conversation policies.
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ErrInvalid wraps every policy validation failure.
var ErrInvalid = errors.New("policy: invalid")

// Validate checks p against conloomapi.GroupPolicy.Validate.
func Validate(p conloomapi.GroupPolicy) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

type Service struct {
	st       store.Store
	defaults conloomapi.GroupPolicy
	private  *conloomapi.GroupPolicy

	// mu guards cache and serialises version creation, so a cache fill never stores a stale version.
	mu       sync.Mutex
	cache    map[string]store.PolicyVersion
	onChange []func(convKey string)
}

// NewService returns a service whose conversations default to defaults.
func NewService(st store.Store, defaults conloomapi.GroupPolicy) *Service {
	return &Service{st: st, defaults: defaults, cache: map[string]store.PolicyVersion{}}
}

// WithPrivateDefaults makes private conversations default to p instead of the
// group defaults, and returns s. Call it before the service is used.
func (s *Service) WithPrivateDefaults(p conloomapi.GroupPolicy) *Service {
	s.private = &p
	return s
}

// defaultsFor is the default policy of convKey's conversation type.
func (s *Service) defaultsFor(convKey string) conloomapi.GroupPolicy {
	if parts := strings.SplitN(convKey, ":", 4); s.private != nil && len(parts) == 4 && parts[2] == conloomapi.ConvPrivate {
		return *s.private
	}
	return s.defaults
}

// OnChange registers fn to be called after every new policy version.
func (s *Service) OnChange(fn func(convKey string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = append(s.onChange, fn)
}

// Current returns the latest stored policy, or the defaults of the conversation's type (see
// WithPrivateDefaults) as version 0 without writing anything.
// A section or setting missing from a stored version (such as proactive or memory, or the turn-taking,
// quote, max_parts and join settings, in versions written before they existed) is filled from the
// defaults; private_wait and private_max_wait are filled as a pair, only when both are zero (see
// conloomapi.GroupPolicy.WithDefaults).
func (s *Service) Current(ctx context.Context, convKey string) (store.PolicyVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current(ctx, convKey)
}

// Forget drops the conversation's cached policy.
func (s *Service) Forget(convKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, convKey)
}

func (s *Service) current(ctx context.Context, convKey string) (store.PolicyVersion, error) {
	if pv, ok := s.cache[convKey]; ok {
		return pv, nil
	}
	defaults := s.defaultsFor(convKey)
	pv, err := s.st.LatestPolicy(ctx, convKey)
	if errors.Is(err, store.ErrNotFound) {
		pv, err = store.PolicyVersion{ConversationKey: convKey, Policy: defaults}, nil
	}
	if err != nil {
		return store.PolicyVersion{}, err
	}
	pv.Policy = pv.Policy.WithDefaults(defaults)
	s.cache[convKey] = pv
	return pv, nil
}

// Update stores put.Policy as a new version. When put omits the proactive or memory section (see
// conloomapi.PolicyPut.ProactiveOmitted and MemoryOmitted) the group's current section is kept,
// which is the defaults for a group that never configured one; a section that is present is
// validated as given, except that zero turn-taking, quote, max_parts and join settings, and
// private_wait and private_max_wait when both are zero, are filled from the defaults as in Current.
func (s *Service) Update(ctx context.Context, convKey string, put conloomapi.PolicyPut, by string) (store.PolicyVersion, error) {
	return s.change(ctx, convKey, by, "update", func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
		p := put.Policy
		if put.ProactiveOmitted() {
			p.Proactive = cur.Policy.Proactive
		}
		if put.MemoryOmitted() {
			p.Memory = cur.Policy.Memory
		}
		zeroProactive := p.Proactive == (conloomapi.ProactivePolicy{})
		zeroMemory := p.Memory == (conloomapi.MemoryPolicy{})
		p = p.WithDefaults(s.defaultsFor(convKey))
		if zeroProactive {
			p.Proactive = conloomapi.ProactivePolicy{}
		}
		if zeroMemory {
			p.Memory = conloomapi.MemoryPolicy{}
		}
		if err := Validate(p); err != nil {
			return conloomapi.GroupPolicy{}, "", false, err
		}
		return p, put.Reason, true, nil
	})
}

// Pause sets the interactive concurrency to 0; an already paused policy is returned unchanged.
func (s *Service) Pause(ctx context.Context, convKey, by string) (store.PolicyVersion, error) {
	return s.change(ctx, convKey, by, "pause", func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
		if cur.Policy.Concurrency.MaxInteractiveRequests == 0 {
			return cur.Policy, "", false, nil
		}
		p := cur.Policy
		p.Concurrency.MaxInteractiveRequests = 0
		return p, "pause", true, nil
	})
}

// Resume restores the interactive concurrency to the newest non-zero value in the history,
// falling back to the defaults and then to 1. A running policy is returned unchanged.
func (s *Service) Resume(ctx context.Context, convKey, by string) (store.PolicyVersion, error) {
	return s.change(ctx, convKey, by, "resume", func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
		if cur.Policy.Concurrency.MaxInteractiveRequests != 0 {
			return cur.Policy, "", false, nil
		}
		history, err := s.st.ListPolicyVersions(ctx, convKey)
		if err != nil {
			return conloomapi.GroupPolicy{}, "", false, err
		}
		n := s.defaultsFor(convKey).Concurrency.MaxInteractiveRequests
		for _, pv := range history {
			if c := pv.Policy.Concurrency.MaxInteractiveRequests; c != 0 {
				n = c
				break
			}
		}
		if n == 0 {
			n = 1
		}
		p := cur.Policy
		p.Concurrency.MaxInteractiveRequests = n
		return p, "resume", true, nil
	})
}

// SetProactiveEnabled switches the group's proactive replies, changing only
// proactive.enabled of the current policy under the service lock, so a
// concurrent change such as Pause is never overwritten. changed reports
// whether a new version was stored; switching to the current state stores
// nothing and returns the current version.
func (s *Service) SetProactiveEnabled(ctx context.Context, convKey string, on bool, by string) (pv store.PolicyVersion, changed bool, err error) {
	action := "proactive_off"
	if on {
		action = "proactive_on"
	}
	pv, err = s.change(ctx, convKey, by, action, func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
		if cur.Policy.Proactive.Enabled == on {
			return cur.Policy, "", false, nil
		}
		p := cur.Policy
		p.Proactive.Enabled = on
		changed = true
		return p, action, true, nil
	})
	return pv, changed && pv.Version != 0, err
}

// change appends the policy produced by next unless it reports no change (returning the current
// version) or an error (returning no version), then invalidates the
// cache, records a policy.update audit event and notifies OnChange callbacks. An audit failure is
// returned together with the already stored version.
func (s *Service) change(ctx context.Context, convKey, by, action string,
	next func(cur store.PolicyVersion) (p conloomapi.GroupPolicy, reason string, changed bool, err error)) (store.PolicyVersion, error) {
	s.mu.Lock()
	cur, err := s.current(ctx, convKey)
	if err != nil {
		s.mu.Unlock()
		return store.PolicyVersion{}, err
	}
	p, reason, changed, err := next(cur)
	if err != nil {
		s.mu.Unlock()
		return store.PolicyVersion{}, err
	}
	if !changed {
		s.mu.Unlock()
		return cur, nil
	}
	pv, err := s.st.AppendPolicyVersion(ctx, store.PolicyVersion{
		ConversationKey: convKey, Policy: p, Reason: reason, UpdatedBy: by,
	})
	if err != nil {
		s.mu.Unlock()
		return store.PolicyVersion{}, err
	}
	delete(s.cache, convKey)
	callbacks := s.onChange
	s.mu.Unlock()

	detail, _ := json.Marshal(struct {
		Action  string `json:"action"`
		Version int    `json:"version"`
	}{action, pv.Version})
	auditErr := s.st.InsertAudit(ctx, store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: botID(convKey), Kind: "policy.update",
		Actor: by, Target: convKey, Detail: string(detail), CreatedAt: time.Now().UTC(),
	})
	for _, fn := range callbacks {
		fn(convKey)
	}
	if auditErr != nil {
		return pv, fmt.Errorf("policy: audit: %w", auditErr)
	}
	return pv, nil
}

// botID returns the bot instance part of a "bot:platform:type:id" conversation key.
func botID(convKey string) string {
	id, _, _ := strings.Cut(convKey, ":")
	return id
}
