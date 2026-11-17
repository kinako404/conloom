package policy

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// pausedFollowers holds the first PolicyFollowers call, either before it lists
// the followers (listFirst false) or after (listFirst true), until release is
// closed; entered is closed once it holds.
type pausedFollowers struct {
	store.Store
	listFirst bool
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newPausedFollowers(st store.Store, listFirst bool) *pausedFollowers {
	return &pausedFollowers{Store: st, listFirst: listFirst, entered: make(chan struct{}), release: make(chan struct{})}
}

func (p *pausedFollowers) PolicyFollowers(ctx context.Context, convType string) ([]string, error) {
	hold := func() { p.once.Do(func() { close(p.entered); <-p.release }) }
	if !p.listFirst {
		hold()
	}
	keys, err := p.Store.PolicyFollowers(ctx, convType)
	if p.listFirst {
		hold()
	}
	return keys, err
}

func (p *pausedFollowers) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fan-out never listed the followers")
	}
}

func TestPolicy_FanOutSkipsFollowerTurnedIndividual(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	key := seedConversation(t, st, conloomapi.ConvGroup, "10001")
	paused := newPausedFollowers(st, true)
	s := newDefaultsService(t, paused)
	calls := recordCalls(s)

	on := withMemory(conloomapi.DefaultGroupPolicy(), true)
	if _, err := s.SetDefaults(ctx, KindGroup, on, "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	paused.waitEntered(t)
	own := withRecent(on, 40)
	if _, err := s.Update(ctx, key, conloomapi.PolicyPut{Policy: own}, "29999"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	close(paused.release)
	s.Wait()

	trs := calls.transitioned()
	if len(trs) != 1 || !reflect.DeepEqual(trs[0].before, on) || !reflect.DeepEqual(trs[0].after, own) {
		t.Fatalf("transitions = %+v, want only the update's new defaults -> own policy", trs)
	}
	if got := calls.changed(); len(got) != 1 {
		t.Fatalf("OnChange calls = %v, want only the update's", got)
	}
}

func TestPolicy_FanOutSkipsConversationFollowingAfterSave(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	key := seedConversation(t, st, conloomapi.ConvGroup, "10001")
	own := withRecent(withMemory(conloomapi.DefaultGroupPolicy(), true), 40)
	appendVersion(t, st, key, own, false)
	paused := newPausedFollowers(st, false)
	s := newDefaultsService(t, paused)
	calls := recordCalls(s)

	v2 := withMemory(conloomapi.DefaultGroupPolicy(), true)
	if _, err := s.SetDefaults(ctx, KindGroup, v2, "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	paused.waitEntered(t)
	if _, err := s.FollowDefaults(ctx, key, "", "panel:admin"); err != nil {
		t.Fatalf("FollowDefaults: %v", err)
	}
	close(paused.release)
	s.Wait()
	trs := calls.transitioned()
	if len(trs) != 1 || !reflect.DeepEqual(trs[0].before, own) || !reflect.DeepEqual(trs[0].after, v2) {
		t.Fatalf("transitions = %+v, want only FollowDefaults' own policy -> new defaults", trs)
	}

	v3 := withRecent(v2, 33)
	if _, err := s.SetDefaults(ctx, KindGroup, v3, "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults v3: %v", err)
	}
	s.Wait()
	trs = calls.transitioned()
	if len(trs) != 2 || trs[1].key != key || !reflect.DeepEqual(trs[1].before, v2) || !reflect.DeepEqual(trs[1].after, v3) {
		t.Fatalf("transitions = %+v, want a later change notified as v2 -> v3", trs)
	}
}

func TestPolicy_CloseDuringFanOut(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	seedConversation(t, st, conloomapi.ConvGroup, "10001")
	paused := newPausedFollowers(st, true)
	s := NewService(paused, conloomapi.DefaultGroupPolicy())
	calls := recordCalls(s)
	if _, err := s.SetDefaults(ctx, KindGroup, withRecent(conloomapi.DefaultGroupPolicy(), 33), "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	paused.waitEntered(t)
	closed := make(chan struct{})
	go func() {
		s.Close()
		close(closed)
	}()
	for s.ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	close(paused.release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	if len(calls.changed()) != 0 || len(calls.transitioned()) != 0 {
		t.Fatalf("notified after Close: %v, %v", calls.changed(), calls.transitioned())
	}
}

type failingDefaults struct{ store.Store }

var errDefaultsRead = errors.New("disk on fire")

func (failingDefaults) LatestPolicyDefaults(context.Context, string) (store.PolicyDefaults, error) {
	return store.PolicyDefaults{}, errDefaultsRead
}

func TestPolicy_LoadDefaultsError(t *testing.T) {
	s := newDefaultsService(t, failingDefaults{newTestStore(t)})
	before := s.Defaults(KindGroup)
	if err := s.LoadDefaults(context.Background()); !errors.Is(err, errDefaultsRead) {
		t.Fatalf("LoadDefaults = %v, want the store error", err)
	}
	if after := s.Defaults(KindGroup); !reflect.DeepEqual(after, before) {
		t.Fatalf("Defaults(group) = %+v, want unchanged", after)
	}
}

func TestPolicy_FollowDefaultsWithoutStoredPolicy(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	s := newDefaultsService(t, st)
	calls := recordCalls(s)
	pv, err := s.FollowDefaults(ctx, testConvKey, "", "panel:admin")
	if err != nil || pv.Version != 0 || !pv.FollowsDefaults {
		t.Fatalf("FollowDefaults = %+v, %v; want version 0 unchanged", pv, err)
	}
	if list, _ := st.ListPolicyVersions(ctx, testConvKey); len(list) != 0 {
		t.Fatalf("stored %d versions, want none", len(list))
	}
	if len(calls.changed()) != 0 || len(calls.transitioned()) != 0 {
		t.Fatalf("callbacks called: %v, %v", calls.changed(), calls.transitioned())
	}
}

func TestPolicy_ResumeAfterFollowUsesLiveDefaults(t *testing.T) {
	ctx := context.Background()
	s := newDefaultsService(t, newTestStore(t))
	if _, err := s.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: withRecent(conloomapi.DefaultGroupPolicy(), 40)}, "29999"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := s.FollowDefaults(ctx, testConvKey, "", "panel:admin"); err != nil {
		t.Fatalf("FollowDefaults: %v", err)
	}
	if _, err := s.SetDefaults(ctx, KindGroup, withConcurrency(conloomapi.DefaultGroupPolicy(), 2), "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	if pv, err := s.Pause(ctx, testConvKey, "29999"); err != nil || pv.Policy.Concurrency.MaxInteractiveRequests != 0 {
		t.Fatalf("Pause = %+v, %v", pv, err)
	}
	pv, err := s.Resume(ctx, testConvKey, "29999")
	if err != nil || pv.Policy.Concurrency.MaxInteractiveRequests != 2 {
		t.Fatalf("Resume = %+v, %v; want the live defaults' concurrency 2, not the followed snapshot's 1", pv.Policy.Concurrency, err)
	}
}

func TestPolicy_SetDefaultsRejectsUnknownAction(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	s := newDefaultsService(t, st)
	if _, err := s.SetDefaults(ctx, KindGroup, conloomapi.DefaultGroupPolicy(), "", "panel:admin", "delete"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetDefaults action delete = %v, want ErrInvalid", err)
	}
	if list, err := st.ListPolicyDefaults(ctx, KindGroup, 0); err != nil || len(list) != 0 {
		t.Fatalf("ListPolicyDefaults = %v, %v; want empty", list, err)
	}
}
