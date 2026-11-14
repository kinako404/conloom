package gateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const autonomyPath = "/api/v1/conversations/private_20001/autonomy"

var autonomyPrivateKey = store.ConversationKey("bot_main", "qq", conloomapi.ConvPrivate, "private_20001")

// seedAutonomyConvs stores bot_main's private chat with 20001 and group 10001.
func (s *memServer) seedAutonomyConvs(t *testing.T) {
	t.Helper()
	for _, conv := range []store.ConversationRecord{
		{BotInstanceID: "bot_main", ID: "private_20001", Key: autonomyPrivateKey, Type: conloomapi.ConvPrivate, Platform: "qq",
			PlatformConversationID: "20001"},
		{BotInstanceID: "bot_main", ID: "group_10001", Key: memGroupKey, Type: conloomapi.ConvGroup, Platform: "qq",
			PlatformConversationID: "10001"},
	} {
		_, err := s.st.Ingest(context.Background(), store.Ingest{
			Event: store.EventRecord{BotInstanceID: "bot_main", IdempotencyKey: "seed:" + conv.ID, EventID: "evt_seed_" + conv.ID,
				Response: []byte("{}"), CreatedAt: time.Now()},
			Conversation: &conv,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func (s *memServer) autonomyState(t *testing.T, token string) conloomapi.AutonomyState {
	t.Helper()
	r := s.do(t, http.MethodGet, autonomyPath, token, nil, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET autonomy: status %d, body %s", r.status, r.body)
	}
	var st conloomapi.AutonomyState
	decode(t, r.body, &st)
	return st
}

func (s *memServer) autonomyAudits(t *testing.T, kind string) []store.AuditEvent {
	t.Helper()
	events, err := s.st.ListAudit(context.Background(), store.AuditFilter{Kind: kind, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestGatewayAutonomy_Scopes(t *testing.T) {
	ms := newMemServer(t, false)
	ms.seedAutonomyConvs(t)
	ctx := context.Background()
	bar := autonomyPath + "/buffs/" + url.PathEscape("心情")
	status := autonomyPath + "/status"

	expectError(t, ms.do(t, http.MethodGet, autonomyPath, "", nil, nil), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	expectError(t, ms.do(t, http.MethodGet, autonomyPath, adminToken, nil, nil), http.StatusForbidden, conloomapi.CodeForbidden)
	expectError(t, ms.do(t, http.MethodPut, bar, memoryReaderToken, map[string]int{"value": 20}, nil), http.StatusForbidden, conloomapi.CodeForbidden)
	expectError(t, ms.do(t, http.MethodDelete, status, memoryReaderToken, nil, nil), http.StatusForbidden, conloomapi.CodeForbidden)

	st := ms.autonomyState(t, memoryReaderToken)
	if st.Status != nil || len(st.Bars) != 2 || st.Bars[0].Name != "心情" || st.Bars[0].Value != 60 || st.Events == nil ||
		st.Proactive.Timers == nil {
		t.Fatalf("state = %+v, want the default bars at their baselines", st)
	}

	r := ms.do(t, http.MethodPut, bar, memoryToken, map[string]any{"value": 20, "reason": "测试"}, nil)
	if r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != `{"ok":true}` {
		t.Fatalf("PUT bar: status %d, body %s", r.status, r.body)
	}
	st = ms.autonomyState(t, memoryToken)
	if st.Bars[0].Value != 20 || len(st.Events) != 1 || st.Events[0].Source != store.BuffSourceAPI || st.Events[0].Reason != "测试" ||
		st.Events[0].Before != 60 || st.Events[0].After != 20 {
		t.Errorf("state after PUT = %+v", st)
	}
	if audits := ms.autonomyAudits(t, "autonomy.buff"); len(audits) != 1 || audits[0].Actor != "memory-main" ||
		audits[0].Target != autonomyPrivateKey || audits[0].BotInstanceID != "bot_main" {
		t.Errorf("autonomy.buff audits = %+v", audits)
	}

	expectError(t, ms.do(t, http.MethodPut, bar, memoryToken, map[string]int{"value": 101}, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, ms.do(t, http.MethodPut, bar, memoryToken, map[string]string{"reason": "没写值"}, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if e := expectError(t, ms.do(t, http.MethodPut, autonomyPath+"/buffs/"+url.PathEscape("饥饿"), memoryToken, map[string]int{"value": 5}, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent); !strings.Contains(e.Message, "精力") {
		t.Errorf("unknown bar message %q does not list the bars", e.Message)
	}

	// Groups have no hidden state; another bot's token sees no such chat.
	expectError(t, ms.do(t, http.MethodGet, "/api/v1/conversations/group_10001/autonomy", memoryToken, nil, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, ms.do(t, http.MethodDelete, "/api/v1/conversations/group_10001/autonomy/status", memoryToken, nil, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, ms.do(t, http.MethodGet, autonomyPath, "test-memory-other-token", nil, nil), http.StatusNotFound, conloomapi.CodeNotFound)

	until := time.Now().Add(20 * time.Minute)
	if err := ms.st.SetStatus(ctx, store.ConversationStatus{ConversationKey: autonomyPrivateKey, Activity: "洗澡", TaskID: "task_seed",
		StartedAt: time.Now(), UntilAt: until}, &store.ConversationTimer{BotInstanceID: "bot_main", PlatformUserID: "20001", DueAt: until}); err != nil {
		t.Fatal(err)
	}
	if st := ms.autonomyState(t, memoryReaderToken); st.Status == nil || st.Status.Activity != "洗澡" {
		t.Fatalf("status = %+v, want 洗澡", st.Status)
	}
	r = ms.do(t, http.MethodDelete, status, memoryToken, nil, nil)
	if r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != `{"ok":true}` {
		t.Fatalf("DELETE status: status %d, body %s", r.status, r.body)
	}
	if _, err := ms.st.GetStatus(ctx, autonomyPrivateKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("status after DELETE err = %v, want ErrNotFound", err)
	}
	if timers, err := ms.st.ListTimers(ctx, autonomyPrivateKey); err != nil || len(timers) != 0 {
		t.Errorf("timers after DELETE = %+v, %v; want none", timers, err)
	}
	if audits := ms.autonomyAudits(t, "autonomy.status.clear"); len(audits) != 1 || audits[0].Actor != "memory-main" {
		t.Errorf("autonomy.status.clear audits = %+v", audits)
	}
}
