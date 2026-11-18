// Package policy manages versioned per-conversation policies and the versioned
// default policies of group and private conversations.
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ErrInvalid wraps every policy validation failure.
var ErrInvalid = errors.New("policy: invalid")

// Kinds of default policy, one per conversation type.
const (
	KindGroup   = conloomapi.ConvGroup
	KindPrivate = conloomapi.ConvPrivate
)

// defaultsBatch is how many following conversations a default policy change
// notifies between checks for Close.
const defaultsBatch = 100

// Validate checks p against conloomapi.GroupPolicy.Validate.
func Validate(p conloomapi.GroupPolicy) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// KindOf is the kind of default policy convKey's conversation follows:
// KindPrivate for a private conversation, KindGroup otherwise.
func KindOf(convKey string) string {
	if parts := strings.SplitN(convKey, ":", 4); len(parts) == 4 && parts[2] == conloomapi.ConvPrivate {
		return KindPrivate
	}
	return KindGroup
}

// Builtin is the built-in default policy of kind: conloomapi.DefaultPrivatePolicy
// for KindPrivate, conloomapi.DefaultGroupPolicy otherwise.
func Builtin(kind string) conloomapi.GroupPolicy {
	if kind == KindPrivate {
		return conloomapi.DefaultPrivatePolicy()
	}
	return conloomapi.DefaultGroupPolicy()
}

type Service struct {
	st store.Store
	// reserved is what a set context.max_context_tokens must exceed (see
	// WithReservedContextTokens).
	reserved int
	log      *slog.Logger

	// mu guards defaults, cache, the callbacks, saves and joined, and serialises version creation,
	// so a cache fill never stores a stale version.
	mu           sync.Mutex
	defaults     map[string]store.PolicyDefaults
	cache        map[string]store.PolicyVersion
	onChange     []func(convKey string)
	onTransition []func(convKey string, before, after conloomapi.GroupPolicy)
	// saves counts the default policy changes made by SetDefaults.
	saves uint64
	// joined holds, for a conversation following the default policy, the last saves count whose
	// default policy it was notified of: set when FollowDefaults returns it to the default policy,
	// and when a default policy change notified to it delivered a later change's default policy
	// as after. The changes up to that count are already in a transition it saw, so notifying
	// their following conversations skips it.
	joined map[string]uint64

	// ctx is cancelled by Close, which stops notifying conversations of default policy changes.
	ctx    context.Context
	cancel context.CancelFunc
	// wg counts the goroutine notifying conversations of the changes in pending.
	wg sync.WaitGroup
	// fanMu guards pending and fanning, so the changes are notified one at a time, in the order
	// they were made.
	fanMu   sync.Mutex
	pending []defaultsChange
	fanning bool
}

// defaultsChange is a change of a kind's default policy whose following
// conversations are still to be notified, with the callbacks registered when
// it was made.
type defaultsChange struct {
	// save is the saves count of the change.
	save          uint64
	before, after store.PolicyDefaults
	onChange      []func(convKey string)
	onTransition  []func(convKey string, before, after conloomapi.GroupPolicy)
}

// NewService returns a service whose conversations default to defaults, as version 0 of both
// kinds' default policy until LoadDefaults reads the stored ones.
func NewService(st store.Store, defaults conloomapi.GroupPolicy) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{st: st, log: slog.New(slog.DiscardHandler), ctx: ctx, cancel: cancel,
		defaults: map[string]store.PolicyDefaults{
			KindGroup:   {Kind: KindGroup, Policy: defaults},
			KindPrivate: {Kind: KindPrivate, Policy: defaults},
		},
		cache: map[string]store.PolicyVersion{}, joined: map[string]uint64{}}
}

// WithPrivateDefaults makes private conversations default to p instead of the
// group defaults until LoadDefaults reads a stored private default policy, and
// returns s. Call it before the service is used.
func (s *Service) WithPrivateDefaults(p conloomapi.GroupPolicy) *Service {
	s.defaults[KindPrivate] = store.PolicyDefaults{Kind: KindPrivate, Policy: p}
	return s
}

// WithReservedContextTokens makes Update and SetDefaults refuse a context.max_context_tokens
// that is set but not more than n, the part of the window the short model
// keeps for its output and safety margin (config.Config.ReservedContextTokens),
// and returns s. Call it before the service is used.
func (s *Service) WithReservedContextTokens(n int) *Service {
	s.reserved = n
	return s
}

// WithLogger makes s log failures to notify conversations of a default policy
// change to l, and returns s; by default it logs nothing. Call it before the
// service is used.
func (s *Service) WithLogger(l *slog.Logger) *Service {
	s.log = l
	return s
}

// LoadDefaults replaces the default policy of each kind with its latest stored
// version; a kind without one keeps its current default policy. Call it once,
// before the service is used.
func (s *Service) LoadDefaults(ctx context.Context) error {
	loaded := map[string]store.PolicyDefaults{}
	for _, kind := range []string{KindGroup, KindPrivate} {
		d, err := s.st.LatestPolicyDefaults(ctx, kind)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("policy: load the %s default policy: %w", kind, err)
		}
		loaded[kind] = d
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for kind, d := range loaded {
		s.defaults[kind] = d
	}
	s.cache = map[string]store.PolicyVersion{}
	return nil
}

// Defaults returns the current default policy of kind, of KindGroup for an unknown kind.
func (s *Service) Defaults(kind string) store.PolicyDefaults {
	if kind != KindPrivate {
		kind = KindGroup
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaults[kind]
}

// defaultsFor is the default policy of convKey's conversation type. s.mu must be held.
func (s *Service) defaultsFor(convKey string) conloomapi.GroupPolicy {
	return s.defaults[KindOf(convKey)].Policy
}

// checkContextCap refuses a context.max_context_tokens that leaves no room for a prompt (see
// WithReservedContextTokens).
func (s *Service) checkContextCap(p conloomapi.GroupPolicy) error {
	if n := p.Context.MaxContextTokens; n > 0 && n <= s.reserved {
		return fmt.Errorf("%w: context.max_context_tokens must be 0 (no limit) or > %d "+
			"(the short model's reserved output and safety margin), got %d", ErrInvalid, s.reserved, n)
	}
	return nil
}

// SetDefaults stores p as a new version of kind's default policy and makes it the default policy
// of the conversations that follow it at once. p is stored as given; an unknown kind, an action
// other than update or restore, a p that fails Validate or a context.max_context_tokens refused as
// in Update is ErrInvalid and stores nothing. It records a settings.policy.update audit event with
// action, and notifies the following conversations (see PolicyFollowers) in the background, in
// batches and in the order of the changes, after it returns (see Wait and Close): each that still
// follows the default policy when its turn comes, and has not yet been notified of this change's
// default policy or a later one (by FollowDefaults after this change, or by an earlier change's
// notification that re-read a later default policy), gets the OnChange callbacks, then the
// OnTransition callbacks with the previous default policy and its effective policy re-read then.
// So the default policy changes notified to a conversation form one chain, each before being the
// default policy of the previous after. An audit failure is returned together with the already stored version.
func (s *Service) SetDefaults(ctx context.Context, kind string, p conloomapi.GroupPolicy, reason, by, action string) (store.PolicyDefaults, error) {
	if kind != KindGroup && kind != KindPrivate {
		return store.PolicyDefaults{}, fmt.Errorf("%w: unknown default policy kind %q", ErrInvalid, kind)
	}
	if action != "update" && action != "restore" {
		return store.PolicyDefaults{}, fmt.Errorf("%w: unknown default policy action %q", ErrInvalid, action)
	}
	if err := Validate(p); err != nil {
		return store.PolicyDefaults{}, err
	}
	if err := s.checkContextCap(p); err != nil {
		return store.PolicyDefaults{}, err
	}
	s.mu.Lock()
	d, err := s.st.AppendPolicyDefaults(ctx, store.PolicyDefaults{Kind: kind, Policy: p, Reason: reason, UpdatedBy: by})
	if err != nil {
		s.mu.Unlock()
		return store.PolicyDefaults{}, err
	}
	before := s.defaults[kind]
	s.defaults[kind] = d
	s.cache = map[string]store.PolicyVersion{}
	s.saves++
	s.enqueue(defaultsChange{save: s.saves, before: before, after: d, onChange: s.onChange, onTransition: s.onTransition})
	s.mu.Unlock()

	detail, _ := json.Marshal(struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
		Action  string `json:"action"`
	}{kind, d.Version, action})
	if err := s.st.InsertAudit(ctx, store.AuditEvent{
		ID: ids.New("aud"), Kind: "settings.policy.update",
		Actor: by, Target: kind, Detail: string(detail), CreatedAt: time.Now().UTC(),
	}); err != nil {
		return d, fmt.Errorf("policy: audit: %w", err)
	}
	return d, nil
}

// enqueue queues c for notifying, starting the notifying goroutine unless it runs or s is closed.
// s.mu must be held, so changes are queued in the order they were made.
func (s *Service) enqueue(c defaultsChange) {
	s.fanMu.Lock()
	defer s.fanMu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	s.pending = append(s.pending, c)
	if !s.fanning {
		s.fanning = true
		s.wg.Add(1)
		go s.fanOut()
	}
}

// fanOut notifies the conversations of the queued changes until none is left or s is closed.
func (s *Service) fanOut() {
	defer s.wg.Done()
	for {
		s.fanMu.Lock()
		if len(s.pending) == 0 || s.ctx.Err() != nil {
			s.pending, s.fanning = nil, false
			s.fanMu.Unlock()
			return
		}
		c := s.pending[0]
		s.pending = s.pending[1:]
		s.fanMu.Unlock()
		s.notifyFollowers(c)
	}
}

// notifyFollowers calls c's callbacks for each conversation that follows c's kind's default policy.
func (s *Service) notifyFollowers(c defaultsChange) {
	keys, err := s.st.PolicyFollowers(s.ctx, c.after.Kind)
	if err != nil {
		if s.ctx.Err() == nil {
			s.log.Error("policy: list the conversations following the default policy; they keep running "+
				"on their previous policy until it next changes", "kind", c.after.Kind, "version", c.after.Version, "err", err)
		}
		return
	}
	for start := 0; start < len(keys); start += defaultsBatch {
		if s.ctx.Err() != nil {
			return
		}
		for _, key := range keys[start:min(start+defaultsBatch, len(keys))] {
			after, ok := s.followingAfter(c, key)
			if !ok {
				continue
			}
			for _, fn := range c.onChange {
				fn(key)
			}
			for _, fn := range c.onTransition {
				fn(key, c.before.Policy, after)
			}
		}
	}
}

// followingAfter re-reads the conversation listed as following c's kind's default policy and
// returns its effective policy now. It reports false, so c is not notified to it, when it no
// longer follows the default policy (a change of its own already notified that), when it was
// already notified of c's default policy or a later one (by FollowDefaults after c was made, or
// by an earlier change's notification that re-read a later default policy), or when it cannot be
// read. When the policy re-read is a later change's, it records that change in joined, so the
// later changes' notifications skip it and its transitions form one chain.
func (s *Service) followingAfter(c defaultsChange, key string) (conloomapi.GroupPolicy, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if save, ok := s.joined[key]; ok {
		if save >= c.save {
			return conloomapi.GroupPolicy{}, false
		}
		delete(s.joined, key)
	}
	pv, err := s.current(s.ctx, key)
	if err != nil {
		if s.ctx.Err() == nil {
			s.log.Error("policy: read a conversation following the default policy; it keeps running on its previous "+
				"policy until it next changes", "conversation", key, "kind", c.after.Kind, "version", c.after.Version, "err", err)
		}
		return conloomapi.GroupPolicy{}, false
	}
	if !pv.FollowsDefaults {
		return conloomapi.GroupPolicy{}, false
	}
	if s.saves > c.save {
		s.joined[key] = s.saves
	}
	return pv.Policy, true
}

// Wait waits until the conversations of every default policy change made so far are notified.
// Call it only while no SetDefaults runs concurrently.
func (s *Service) Wait() {
	s.wg.Wait()
}

// Close stops notifying conversations of default policy changes, skipping the
// rest of the current one and any later one, and waits for the notifying in
// progress to stop.
func (s *Service) Close() {
	s.fanMu.Lock()
	s.cancel()
	s.fanMu.Unlock()
	s.wg.Wait()
}

// OnChange registers fn to be called after every new policy version of a conversation, and for
// each conversation that follows a default policy (see SetDefaults) after that changes.
func (s *Service) OnChange(fn func(convKey string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = append(s.onChange, fn)
}

// OnTransition registers fn to be called whenever a conversation's effective policy may have
// changed, with the effective policies (as returned by Current) before and after: after every new
// policy version of the conversation, following the OnChange callbacks, and for each conversation
// that follows a default policy after that changes.
func (s *Service) OnTransition(fn func(convKey string, before, after conloomapi.GroupPolicy)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTransition = append(s.onTransition, fn)
}

// Current returns the conversation's effective policy. A conversation without a stored policy
// follows the default policy of its type (see Defaults): it gets that as version 0 with
// FollowsDefaults, without writing anything. When its latest stored version has FollowsDefaults
// it gets that version with the default policy in place of the stored one. Otherwise it gets its
// latest stored version, where a section or setting missing (such as proactive or memory, or the
// turn-taking, quote, max_parts and join settings, in versions written before they existed) is
// filled from the defaults; max_parts only when its key is missing (an explicit 0 is kept), and
// private_wait and private_max_wait as a pair, only when both are zero (see
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
	switch {
	case errors.Is(err, store.ErrNotFound):
		pv = store.PolicyVersion{ConversationKey: convKey, Policy: defaults, FollowsDefaults: true}
	case err != nil:
		return store.PolicyVersion{}, err
	case pv.FollowsDefaults:
		pv.Policy = defaults
	default:
		pv.Policy = pv.Policy.WithDefaults(defaults)
	}
	s.cache[convKey] = pv
	return pv, nil
}

// Update stores put.Policy as a new version, which no longer follows the default policy. When put
// omits the proactive or memory section (see
// conloomapi.PolicyPut.ProactiveOmitted and MemoryOmitted) the group's current section is kept,
// which is the defaults for a group that never configured one or follows the defaults; a section
// that is present is validated as given, except that zero turn-taking, quote and join settings, a
// max_parts missing from the request, and private_wait and private_max_wait when both are zero, are
// filled from the defaults as in Current. A context.max_context_tokens that leaves no room for a prompt (see
// WithReservedContextTokens) is ErrInvalid.
func (s *Service) Update(ctx context.Context, convKey string, put conloomapi.PolicyPut, by string) (store.PolicyVersion, error) {
	return s.change(ctx, convKey, by, "update", false, func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
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
		if err := s.checkContextCap(p); err != nil {
			return conloomapi.GroupPolicy{}, "", false, err
		}
		return p, put.Reason, true, nil
	})
}

// FollowDefaults returns the conversation to the default policy of its type: it stores the current
// default policy as a new version with FollowsDefaults, with reason or "改回跟随默认" when that is
// empty, after which Current follows the default policy as for a conversation that never stored
// one. A conversation that already follows it is returned unchanged.
func (s *Service) FollowDefaults(ctx context.Context, convKey, reason, by string) (store.PolicyVersion, error) {
	if reason == "" {
		reason = "改回跟随默认"
	}
	return s.change(ctx, convKey, by, "follow_defaults", true, func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
		if cur.FollowsDefaults {
			return cur.Policy, "", false, nil
		}
		return s.defaultsFor(convKey), reason, true, nil
	})
}

// Pause sets the interactive concurrency to 0; an already paused policy is returned unchanged.
func (s *Service) Pause(ctx context.Context, convKey, by string) (store.PolicyVersion, error) {
	return s.change(ctx, convKey, by, "pause", false, func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
		if cur.Policy.Concurrency.MaxInteractiveRequests == 0 {
			return cur.Policy, "", false, nil
		}
		p := cur.Policy
		p.Concurrency.MaxInteractiveRequests = 0
		return p, "pause", true, nil
	})
}

// Resume restores the interactive concurrency to the newest non-zero value in the history, where a
// version that follows the default policy counts with the current default policy's, falling back
// to the defaults and then to 1. A running policy is returned unchanged.
func (s *Service) Resume(ctx context.Context, convKey, by string) (store.PolicyVersion, error) {
	return s.change(ctx, convKey, by, "resume", false, func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
		if cur.Policy.Concurrency.MaxInteractiveRequests != 0 {
			return cur.Policy, "", false, nil
		}
		history, err := s.st.ListPolicyVersions(ctx, convKey)
		if err != nil {
			return conloomapi.GroupPolicy{}, "", false, err
		}
		def := s.defaultsFor(convKey).Concurrency.MaxInteractiveRequests
		n := def
		for _, pv := range history {
			c := pv.Policy.Concurrency.MaxInteractiveRequests
			if pv.FollowsDefaults {
				c = def
			}
			if c != 0 {
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
	pv, err = s.change(ctx, convKey, by, action, false, func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
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

// SetPrivateLevel sets how often the bot starts the private chat, changing
// only proactive.private.level of the current policy under the service lock,
// as SetProactiveEnabled does. level must be a PrivateLevel* value (empty for
// the default) and convKey a private chat's, or ErrInvalid is returned. changed reports whether a new
// version was stored; setting the current level stores nothing and returns the
// current version.
func (s *Service) SetPrivateLevel(ctx context.Context, convKey, level, by string) (pv store.PolicyVersion, changed bool, err error) {
	if KindOf(convKey) != KindPrivate {
		return store.PolicyVersion{}, false, fmt.Errorf("%w: proactive.private.level applies to private conversations only", ErrInvalid)
	}
	action := "private_level_" + level
	pv, err = s.change(ctx, convKey, by, action, false, func(cur store.PolicyVersion) (conloomapi.GroupPolicy, string, bool, error) {
		if cur.Policy.Proactive.Private.Level == level {
			return cur.Policy, "", false, nil
		}
		p := cur.Policy
		p.Proactive.Private.Level = level
		if err := Validate(p); err != nil {
			return conloomapi.GroupPolicy{}, "", false, err
		}
		changed = true
		return p, action, true, nil
	})
	return pv, changed && pv.Version != 0, err
}

// change appends the policy produced by next, with FollowsDefaults set to follows, unless next
// reports no change (returning the current version) or an error (returning no version). next gets
// the current effective version, whose policy is the default policy for a conversation that
// follows it. change then invalidates the cache, records a policy.update audit event and calls the
// OnChange callbacks, then the OnTransition callbacks with the effective policies before and after.
// An audit failure is returned together with the already stored version.
func (s *Service) change(ctx context.Context, convKey, by, action string, follows bool,
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
		ConversationKey: convKey, Policy: p, Reason: reason, UpdatedBy: by, FollowsDefaults: follows,
	})
	if err != nil {
		s.mu.Unlock()
		return store.PolicyVersion{}, err
	}
	delete(s.cache, convKey)
	if follows {
		s.joined[convKey] = s.saves
	} else {
		delete(s.joined, convKey)
	}
	before, after := cur.Policy, pv.Policy.WithDefaults(s.defaultsFor(convKey))
	if eff, err := s.current(ctx, convKey); err == nil {
		after = eff.Policy
	}
	callbacks, transitions := s.onChange, s.onTransition
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
	for _, fn := range transitions {
		fn(convKey, before, after)
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
