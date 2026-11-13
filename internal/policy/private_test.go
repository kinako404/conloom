package policy

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var testPrivateKey = store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "private_20001")

func privateDefaults() conloomapi.GroupPolicy {
	p := conloomapi.DefaultGroupPolicy()
	p.Context.MaxRecentMessages = 200
	p.Concurrency.MaxInteractiveRequests = 2
	p.Memory.Enabled, p.Memory.ConsolidateEvery, p.Memory.ConsolidateMin = true, 40, 6
	p.Memory.ConsolidateInterval = conloomapi.Duration{Duration: time.Hour}
	return p
}

func TestPolicy_PrivateConversationsUsePrivateDefaults(t *testing.T) {
	ctx := context.Background()
	group := conloomapi.DefaultGroupPolicy()
	s := NewService(newTestStore(t), group).WithPrivateDefaults(privateDefaults())
	pv, err := s.Current(ctx, testPrivateKey)
	if err != nil || pv.Version != 0 || !reflect.DeepEqual(pv.Policy, privateDefaults()) {
		t.Fatalf("private Current = %+v, %v; want the private defaults", pv, err)
	}
	pv, err = s.Current(ctx, testConvKey)
	if err != nil || !reflect.DeepEqual(pv.Policy, group) {
		t.Fatalf("group Current = %+v, %v; want the group defaults", pv, err)
	}

	plain := NewService(newTestStore(t), group)
	if pv, err := plain.Current(ctx, testPrivateKey); err != nil || !reflect.DeepEqual(pv.Policy, group) {
		t.Fatalf("private Current without private defaults = %+v, %v; want the group defaults", pv, err)
	}
}

func TestPolicy_PrivateStoredVersionWins(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	stored := conloomapi.DefaultGroupPolicy()
	stored.Context.MaxRecentMessages = 30
	if _, err := st.AppendPolicyVersion(ctx, store.PolicyVersion{ConversationKey: testPrivateKey, Policy: stored, UpdatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	s := NewService(st, conloomapi.DefaultGroupPolicy()).WithPrivateDefaults(privateDefaults())
	pv, err := s.Current(ctx, testPrivateKey)
	if err != nil || pv.Version != 1 || !reflect.DeepEqual(pv.Policy, stored) {
		t.Fatalf("Current = %+v, %v; want the stored version", pv, err)
	}
}

func TestPolicy_PrivateLegacyVersionFilledFromPrivateDefaults(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	raw, err := json.Marshal(conloomapi.DefaultGroupPolicy())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "memory")
	legacyJSON, _ := json.Marshal(doc)
	var legacy conloomapi.GroupPolicy
	if err := json.Unmarshal(legacyJSON, &legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendPolicyVersion(ctx, store.PolicyVersion{ConversationKey: testPrivateKey, Policy: legacy, UpdatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	s := NewService(st, conloomapi.DefaultGroupPolicy()).WithPrivateDefaults(privateDefaults())
	pv, err := s.Current(ctx, testPrivateKey)
	if err != nil || pv.Policy.Memory != privateDefaults().Memory {
		t.Fatalf("legacy private memory = %+v, %v; want %+v", pv.Policy.Memory, err, privateDefaults().Memory)
	}
}

func TestPolicy_PrivateUpdateOmittedMemoryKeepsPrivateDefaults(t *testing.T) {
	ctx := context.Background()
	s := NewService(newTestStore(t), conloomapi.DefaultGroupPolicy()).WithPrivateDefaults(privateDefaults())
	p := privateDefaults()
	p.Context.MaxRecentMessages = 120
	p.Memory = conloomapi.MemoryPolicy{}
	pv, err := s.Update(ctx, testPrivateKey, conloomapi.PolicyPut{Policy: p, Reason: "test"}, "admin")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if pv.Policy.Memory != privateDefaults().Memory || pv.Policy.Context.MaxRecentMessages != 120 {
		t.Fatalf("Update = %+v, want the private memory kept", pv.Policy)
	}
}

func TestPolicy_PrivateResumeFallsBackToPrivateDefaults(t *testing.T) {
	ctx := context.Background()
	s := NewService(newTestStore(t), conloomapi.DefaultGroupPolicy()).WithPrivateDefaults(privateDefaults())
	if _, err := s.Pause(ctx, testPrivateKey, "admin"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	pv, err := s.Resume(ctx, testPrivateKey, "admin")
	if err != nil || pv.Policy.Concurrency.MaxInteractiveRequests != 2 {
		t.Fatalf("Resume = %+v, %v; want the private default concurrency 2", pv.Policy.Concurrency, err)
	}
}
