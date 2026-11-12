// Package policy manages versioned per-conversation group policies.
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

	// mu guards cache and serialises version creation, so a cache fill never stores a stale version.
	mu       sync.Mutex
	cache    map[string]store.PolicyVersion
	onChange []func(convKey string)
}

func NewService(st store.Store, defaults conloomapi.GroupPolicy) *Service {
	return &Service{st: st, defaults: defaults, cache: map[string]store.PolicyVersion{}}
}

// OnChange registers fn to be called after every new policy version.
func (s *Service) OnChange(fn func(convKey string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = append(s.onChange, fn)
}

// Current returns the latest stored policy, or the defaults as version 0 without writing anything.
// A section missing from a stored version (such as proactive in versions written before it existed)
// is filled from the defaults.
func (s *Service) Current(ctx context.Context, convKey string) (store.PolicyVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current(ctx, convKey)
}

func (s *Service) current(ctx context.Context, convKey string) (store.PolicyVersion, error) {
	if pv, ok := s.cache[convKey]; ok {
		return pv, nil
	}
	pv, err := s.st.LatestPolicy(ctx, convKey)
	if errors.Is(err, store.ErrNotFound) {
		pv, err = store.PolicyVersion{ConversationKey: convKey, Policy: s.defaults}, nil
	}
	if err != nil {
		return store.PolicyVersion{}, err
	}
	pv.Policy = pv.Policy.WithDefaults(s.defaults)
	s.cache[convKey] = pv
	return pv, nil
}

// Update stores put.Policy as a new version; a missing proactive section is filled from the defaults.
func (s *Service) Update(ctx context.Context, convKey string, put conloomapi.PolicyPut, by string) (store.PolicyVersion, error) {
	p := put.Policy.WithDefaults(s.defaults)
	if err := Validate(p); err != nil {
		return store.PolicyVersion{}, err
	}
	return s.change(ctx, convKey, by, "update", func(store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
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
		n := s.defaults.Concurrency.MaxInteractiveRequests
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

// change appends the policy produced by next unless it reports no change, then invalidates the
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
	if err != nil || !changed {
		s.mu.Unlock()
		return cur, err
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
