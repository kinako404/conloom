package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var (
	groupKey   = store.ConversationKey("bot_test", "qq", conloomapi.ConvGroup, "group_10001")
	otherKey   = store.ConversationKey("bot_test", "qq", conloomapi.ConvGroup, "group_10002")
	privateKey = store.ConversationKey("bot_test", "qq", conloomapi.ConvPrivate, "private_20001")
	shanghai   = mustLocation("Asia/Shanghai")
	// testNow is Friday 2026-11-13 16:30 in Asia/Shanghai.
	testNow = time.Date(2026, 11, 13, 8, 30, 0, 0, time.UTC)
)

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func newStore(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newPolicies returns a policy service whose conversations default to memory
// on or off, with affinity on or off.
func newPolicies(st store.Store, memoryOn, affinity bool) *policy.Service {
	p := conloomapi.DefaultGroupPolicy()
	p.Memory.Enabled, p.Memory.Affinity = memoryOn, affinity
	return policy.NewService(st, p)
}

func invocation(convKey, user, name string) skills.Invocation {
	convType := conloomapi.ConvGroup
	if strings.Contains(convKey, ":private:") {
		convType = conloomapi.ConvPrivate
	}
	return skills.Invocation{TaskID: "task_test", BotInstanceID: "bot_test", ConversationKey: convKey,
		ConversationType: convType, PlatformUserID: user, DisplayName: name, Now: testNow, Location: shanghai}
}

func call(t *testing.T, tool skills.Tool, inv skills.Invocation, args string) string {
	t.Helper()
	got, err := tool.Call(context.Background(), inv, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s(%s): %v", tool.Spec().Name, args, err)
	}
	return got
}

// checkSpec checks that spec is named name, has a description and that its
// parameters are an object schema requiring exactly required.
func checkSpec(t *testing.T, spec skills.Spec, name string, required ...string) {
	t.Helper()
	if spec.Name != name || strings.TrimSpace(spec.Description) == "" {
		t.Errorf("spec = %+v, want name %q and a description", spec, name)
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
		t.Fatalf("parameters %s: %v", spec.Parameters, err)
	}
	if schema.Type != "object" || schema.Properties == nil || fmt.Sprint(schema.Required) != fmt.Sprint(required) && len(required) > 0 {
		t.Errorf("parameters = %s, want an object requiring %v", spec.Parameters, required)
	}
	if len(required) == 0 && (len(schema.Properties) != 0 || len(schema.Required) != 0) {
		t.Errorf("parameters = %s, want no properties", spec.Parameters)
	}
	for _, r := range required {
		if _, ok := schema.Properties[r]; !ok {
			t.Errorf("parameters = %s lack property %q", spec.Parameters, r)
		}
	}
}

var msgCount int

// message is a member message (or the bot's when user is "") in convKey.
func message(convKey, user, name, text string, at time.Time) store.MessageRecord {
	msgCount++
	id := fmt.Sprintf("msg_%04d", msgCount)
	m := store.MessageRecord{ID: id, BotInstanceID: "bot_test", ConversationKey: convKey, Platform: "qq",
		PlatformMessageID: "p" + id, ActorID: "user_" + user, PlatformUserID: user, DisplayName: name, Text: text, OccurredAt: at}
	if user == "" {
		m.ActorID, m.PlatformUserID, m.DisplayName, m.FromBot = "bot_30001", "30001", "织语", true
	}
	return m
}

func addMessages(t *testing.T, st store.Store, msgs ...store.MessageRecord) {
	t.Helper()
	for _, m := range msgs {
		ev := store.EventRecord{BotInstanceID: "bot_test", IdempotencyKey: "k_" + m.ID, RequestHash: "h", EventID: "evt_" + m.ID,
			Response: []byte("{}"), RawEvent: []byte("{}")}
		if _, err := st.Ingest(context.Background(), store.Ingest{Event: ev, Message: &m}); err != nil {
			t.Fatalf("Ingest %s: %v", m.ID, err)
		}
	}
}

func intPtr(v int) *int { return &v }

func putMember(t *testing.T, st store.Store, m store.MemberMemory) {
	t.Helper()
	if conflicts, err := st.ApplyMemoryWrite(context.Background(),
		store.MemoryWrite{ConversationKey: m.ConversationKey, Members: []store.MemberMemory{m}}); err != nil || len(conflicts) != 0 {
		t.Fatalf("ApplyMemoryWrite = %v, %v", conflicts, err)
	}
}

func putOf(p conloomapi.GroupPolicy) conloomapi.PolicyPut {
	return conloomapi.PolicyPut{Policy: p, Reason: "test"}
}
