package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/gateway"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/outbox"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	adapterToken = "test-adapter-token"
	adminToken   = "test-admin-token"
	readerToken  = "test-reader-token"
	otherToken   = "test-other-token"
	fakeReply    = "你好呀，我在。"
)

const testConfig = `
server:
  timezone: Asia/Shanghai
  metrics_enabled: true
api_tokens:
  - {name: adapter-main, token: test-adapter-token, bot_instance_id: bot_main, scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]}
  - {name: admin-main, token: test-admin-token, bot_instance_id: bot_main, scopes: [persona:read, policy:write, admin:read]}
  - {name: reader-main, token: test-reader-token, bot_instance_id: bot_main, scopes: [outbox:read]}
  - {name: adapter-other, token: test-other-token, bot_instance_id: bot_other, scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]}
bots:
  - id: bot_main
    platform: qq
    name: 织语
    platform_self_id: "30001"
    admins: ["29999"]
    enabled_conversations:
      - {type: group, platform_id: "10001"}
    private_chat: admins
  - id: bot_other
    platform: qq
    name: 织语
    platform_self_id: "30001"
    enabled_conversations:
      - {type: group, platform_id: "10001"}
model_profiles:
  short_context:
    endpoint: MODEL_URL
    model: short-model
    api_key: test-key-short
    modalities: [text]
    context: {native_window: 3000, configured_window: 3000, soft_input_budget: 2000, reserved_output_tokens: 256, safety_margin_tokens: 64}
    runtime: {max_concurrency: 2, timeout: 5s, max_retries: 0}
    privacy: {allow_sensitive_content: true}
default_group_policy:
  concurrency: {max_interactive_requests: 1, max_queued_requests: 10, priority: normal, allow_long_requests: true}
  reply: {command_prefix: /, continuation_window: 0s, debounce: 30ms, min_reply_interval: 0s, user_rate_limit_per_minute: 6}
  context: {max_recent_messages: 50, max_context_tokens: 0}
  review: {level: basic, review_input: true, review_output: true, action: block}
`

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type serverOpts struct {
	configure func(*config.Config)
	metrics   http.Handler
}

type testServer struct {
	URL  string
	st   *sqlite.DB
	logs *syncBuffer
	seq  atomic.Int64
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return newTestServerWith(t, serverOpts{})
}

func newTestServerWith(t *testing.T, opts serverOpts) *testServer {
	t.Helper()
	model := fakeopenai.New(t, func(map[string]any) (int, string) { return 200, fakeReply })
	dir := t.TempDir()
	path := filepath.Join(dir, "conloom.yaml")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(testConfig, "MODEL_URL", model.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	if opts.configure != nil {
		opts.configure(cfg)
	}

	st, err := sqlite.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := prometheus.NewRegistry()
	metrics := obs.NewMetrics(reg)
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := cfg.ModelProfiles["short_context"]
	profiles := []*router.Profile{{Role: router.RoleShort, Config: p, Client: openai.New(p, nil)}}
	ob := outbox.New(st, outbox.Config{}, metrics, nil)
	personas := persona.NewService(st, cfg.PersonaTemplates)
	policies := policy.NewService(st, cfg.DefaultGroupPolicy)
	o, err := core.New(core.Deps{
		Store:     st,
		Router:    router.New(profiles, router.BreakerConfig{}, time.Now),
		Admission: admission.New(map[string]admission.Limits{string(router.RoleShort): {MaxConcurrency: 2}}),
		Personas:  personas,
		Policies:  policies,
		Reviewer:  review.New(cfg.Secrets(), review.Options{}),
		Notifier:  ob,
		Metrics:   metrics,
		Logger:    logger,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(o.Close)

	metricsHandler := opts.metrics
	if metricsHandler == nil {
		metricsHandler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	}
	srv := httptest.NewServer(gateway.New(gateway.Deps{
		Orchestrator: o, Outbox: ob, Personas: personas, Policies: policies, Store: st,
		Config: cfg, Metrics: metricsHandler, Logger: logger,
	}).Handler())
	t.Cleanup(srv.Close)
	return &testServer{URL: srv.URL, st: st, logs: logs}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (s *testServer) do(t *testing.T, method, path, token string, body any, header map[string]string) response {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: raw}
}

func (s *testServer) postEvent(t *testing.T, token, key string, ev any) response {
	t.Helper()
	hdr := map[string]string{}
	if key != "" {
		hdr["Idempotency-Key"] = key
	}
	return s.do(t, http.MethodPost, "/api/v1/events", token, ev, hdr)
}

type evOpt func(*conloomapi.Event)

func mentionBot(ev *conloomapi.Event) {
	ev.Message.Mentions = append(ev.Message.Mentions, conloomapi.Mention{Type: "bot", ID: ev.BotInstanceID})
	ev.Trigger = conloomapi.Trigger{Type: conloomapi.TriggerMention, RequestedResponse: true}
}

func forBot(botID string) evOpt {
	return func(ev *conloomapi.Event) { ev.BotInstanceID = botID }
}

func (s *testServer) event(user, text string, opts ...evOpt) conloomapi.Event {
	pmid := strconv.FormatInt(1000+s.seq.Add(1), 10)
	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_" + pmid,
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: "bot_main",
		Platform:      "qq",
		OccurredAt:    time.Now().UTC(),
		Conversation: conloomapi.Conversation{
			Type: conloomapi.ConvGroup, ID: "group_10001", PlatformConversationID: "10001",
		},
		Actor:   conloomapi.Actor{ID: "user_" + user, PlatformUserID: user, DisplayName: "用户" + user},
		Trigger: conloomapi.Trigger{Type: conloomapi.TriggerObserve},
		Message: conloomapi.Message{
			ID: "msg_" + pmid, PlatformMessageID: pmid, Text: text,
			Mentions: []conloomapi.Mention{}, Attachments: []conloomapi.Attachment{},
		},
	}
	for _, o := range opts {
		o(&ev)
	}
	return ev
}

// accept submits ev with token under the key "key:<message id>" and requires a 202.
func (s *testServer) accept(t *testing.T, token string, ev conloomapi.Event) conloomapi.EventAccepted {
	t.Helper()
	r := s.postEvent(t, token, "key:"+ev.Message.ID, ev)
	if r.status != http.StatusAccepted {
		t.Fatalf("POST /events: status %d, body %s", r.status, r.body)
	}
	var acc conloomapi.EventAccepted
	decode(t, r.body, &acc)
	return acc
}

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func expectError(t *testing.T, r response, status int, code string) conloomapi.ErrorBody {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d (body %s)", r.status, status, r.body)
	}
	var raw map[string]map[string]any
	decode(t, r.body, &raw)
	for _, k := range []string{"code", "message", "retryable", "retry_after_ms", "trace_id"} {
		if _, ok := raw["error"][k]; !ok {
			t.Fatalf("error body %s lacks %q", r.body, k)
		}
	}
	var er conloomapi.ErrorResponse
	decode(t, r.body, &er)
	if er.Error.Code != code {
		t.Fatalf("code = %q, want %q (body %s)", er.Error.Code, code, r.body)
	}
	if er.Error.TraceID == "" || er.Error.Message == "" {
		t.Fatalf("error body lacks trace_id or message: %s", r.body)
	}
	return er.Error
}

func TestEvents_RequiresAuth(t *testing.T) {
	s := newTestServer(t)
	ev := s.event("20001", "大家好")
	r := s.postEvent(t, "", "k1", ev)
	body := expectError(t, r, http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	if !strings.HasPrefix(body.TraceID, "trace_") {
		t.Fatalf("trace_id = %q", body.TraceID)
	}
	expectError(t, s.postEvent(t, "wrong-token", "k1", ev), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	r = s.do(t, http.MethodPost, "/api/v1/events", "", ev, map[string]string{
		"Idempotency-Key": "k1", "Authorization": "Basic " + adapterToken,
	})
	expectError(t, r, http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	r = s.do(t, http.MethodPost, "/api/v1/events", "", ev, map[string]string{
		"Idempotency-Key": "k1", "Authorization": "bearer " + adapterToken,
	})
	if r.status != http.StatusAccepted {
		t.Fatalf("lower-case bearer scheme: status %d, body %s", r.status, r.body)
	}
}

func TestEvents_ScopeEnforced(t *testing.T) {
	s := newTestServer(t)
	expectError(t, s.postEvent(t, readerToken, "k1", s.event("20001", "大家好")), http.StatusForbidden, conloomapi.CodeForbidden)
	expectError(t, s.do(t, http.MethodGet, "/api/v1/tasks/task_x", readerToken, nil, nil), http.StatusForbidden, conloomapi.CodeForbidden)
	expectError(t, s.do(t, http.MethodPost, "/api/v1/outbox/act_x/ack", readerToken,
		conloomapi.AckRequest{Status: conloomapi.AckSent}, nil), http.StatusForbidden, conloomapi.CodeForbidden)
}

func TestEvents_CrossBotForbidden(t *testing.T) {
	s := newTestServer(t)
	expectError(t, s.postEvent(t, otherToken, "k1", s.event("20001", "大家好")), http.StatusForbidden, conloomapi.CodeForbidden)
	if r := s.postEvent(t, otherToken, "k1", s.event("20001", "大家好", forBot("bot_other"))); r.status != http.StatusAccepted {
		t.Fatalf("own bot: status %d, body %s", r.status, r.body)
	}
}

func TestEvents_MissingIdempotencyKey(t *testing.T) {
	s := newTestServer(t)
	expectError(t, s.postEvent(t, adapterToken, "", s.event("20001", "大家好")), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
}

func TestEvents_InvalidSchemaVersion(t *testing.T) {
	s := newTestServer(t)
	expectError(t, s.postEvent(t, adapterToken, "k-schema", s.event("20001", "大家好", func(ev *conloomapi.Event) {
		ev.SchemaVersion = "2.0"
	})), http.StatusBadRequest, conloomapi.CodeInvalidEvent)

	cases := map[string]evOpt{
		"event type":        func(ev *conloomapi.Event) { ev.EventType = "message.deleted" },
		"conversation type": func(ev *conloomapi.Event) { ev.Conversation.Type = "channel" },
		"conversation id":   func(ev *conloomapi.Event) { ev.Conversation.ID = "" },
		"platform conv id":  func(ev *conloomapi.Event) { ev.Conversation.PlatformConversationID = "" },
		"actor id":          func(ev *conloomapi.Event) { ev.Actor.ID = "" },
		"platform user id":  func(ev *conloomapi.Event) { ev.Actor.PlatformUserID = "" },
		"message id":        func(ev *conloomapi.Event) { ev.Message.ID = "" },
		"platform mismatch": func(ev *conloomapi.Event) { ev.Platform = "other" },
		"text over 16000":   func(ev *conloomapi.Event) { ev.Message.Text = strings.Repeat("字", 16001) },
		"blank message id":  func(ev *conloomapi.Event) { ev.Message.ID = "  " },
		"empty platform":    func(ev *conloomapi.Event) { ev.Platform = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			expectError(t, s.postEvent(t, adapterToken, "k-"+name, s.event("20001", "大家好", mutate)),
				http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		})
	}
	t.Run("text of exactly 16000 runes", func(t *testing.T) {
		ev := s.event("20001", strings.Repeat("字", 16000))
		if r := s.postEvent(t, adapterToken, "k-max", ev); r.status != http.StatusAccepted {
			t.Fatalf("status %d, body %s", r.status, r.body)
		}
	})
	t.Run("malformed json", func(t *testing.T) {
		expectError(t, s.postEvent(t, adapterToken, "k-json", `{"schema_version":`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	})
	t.Run("body over 1 MiB", func(t *testing.T) {
		big := []byte(`{"pad":"` + strings.Repeat("a", 1<<20) + `"}`)
		expectError(t, s.postEvent(t, adapterToken, "k-big", big), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	})
}

func TestEvents_ReplayHeader(t *testing.T) {
	s := newTestServer(t)
	ev := s.event("20001", "大家好")
	first := s.postEvent(t, adapterToken, "k1", ev)
	if first.status != http.StatusAccepted || first.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("first: status %d, header %q, body %s", first.status, first.header.Get("Idempotent-Replayed"), first.body)
	}
	second := s.postEvent(t, adapterToken, "k1", ev)
	if second.status != http.StatusAccepted || second.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("second: status %d, header %q, body %s", second.status, second.header.Get("Idempotent-Replayed"), second.body)
	}
	var a, b conloomapi.EventAccepted
	decode(t, first.body, &a)
	decode(t, second.body, &b)
	if a != b || a.Disposition != conloomapi.DispositionObserve || a.EventID != ev.EventID {
		t.Fatalf("first %+v, replay %+v", a, b)
	}
}

func TestEvents_ConflictIs409(t *testing.T) {
	s := newTestServer(t)
	ev := s.event("20001", "大家好")
	s.accept(t, adapterToken, ev)
	changed := ev
	changed.Message.Text = "改过的内容"
	expectError(t, s.postEvent(t, adapterToken, "key:"+ev.Message.ID, changed), http.StatusConflict, conloomapi.CodeDuplicateEvent)
	expectError(t, s.postEvent(t, adapterToken, "another-key", ev), http.StatusConflict, conloomapi.CodeDuplicateEvent)
}

func (s *testServer) poll(t *testing.T, token, query string) []conloomapi.Action {
	t.Helper()
	r := s.do(t, http.MethodGet, "/api/v1/outbox"+query, token, nil, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET /outbox%s: status %d, body %s", query, r.status, r.body)
	}
	if !bytes.Contains(r.body, []byte(`"actions":[`)) {
		t.Fatalf("GET /outbox%s: actions not an array: %s", query, r.body)
	}
	var out conloomapi.OutboxResponse
	decode(t, r.body, &out)
	return out.Actions
}

func TestOutbox_PollAndAckFlow(t *testing.T) {
	s := newTestServer(t)
	acc := s.accept(t, adapterToken, s.event("20001", "在吗", mentionBot))
	if acc.Disposition != conloomapi.DispositionReply || acc.TaskID == "" {
		t.Fatalf("accepted %+v", acc)
	}

	expectError(t, s.do(t, http.MethodGet, "/api/v1/outbox?wait=soon", adapterToken, nil, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, s.do(t, http.MethodGet, "/api/v1/outbox?wait=-1s", adapterToken, nil, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, s.do(t, http.MethodGet, "/api/v1/outbox?limit=many", adapterToken, nil, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)

	actions := s.poll(t, adapterToken, "?limit=0&wait=5s")
	if len(actions) != 1 || actions[0].Content.Text != fakeReply || actions[0].ConversationID != "group_10001" {
		t.Fatalf("actions = %+v", actions)
	}
	id := actions[0].ActionID

	r := s.do(t, http.MethodGet, "/api/v1/tasks/"+acc.TaskID, adapterToken, nil, nil)
	var task conloomapi.Task
	decode(t, r.body, &task)
	if r.status != http.StatusOK || task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("GET task: status %d, body %s", r.status, r.body)
	}
	r = s.do(t, http.MethodPost, "/api/v1/tasks/"+acc.TaskID+"/cancel", adapterToken, nil, nil)
	decode(t, r.body, &task)
	if r.status != http.StatusOK || task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("cancel finished task: status %d, body %s", r.status, r.body)
	}

	expectError(t, s.do(t, http.MethodPost, "/api/v1/outbox/"+id+"/ack", adapterToken,
		conloomapi.AckRequest{Status: "delivered"}, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, s.do(t, http.MethodPost, "/api/v1/outbox/"+id+"/ack", adapterToken, "{", nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, s.do(t, http.MethodPost, "/api/v1/outbox/act_missing/ack", adapterToken,
		conloomapi.AckRequest{Status: conloomapi.AckSent}, nil), http.StatusNotFound, conloomapi.CodeNotFound)

	ack := conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "5001"}
	for i := 0; i < 2; i++ {
		r = s.do(t, http.MethodPost, "/api/v1/outbox/"+id+"/ack", adapterToken, ack, nil)
		if r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != `{"ok":true}` {
			t.Fatalf("ack %d: status %d, body %s", i, r.status, r.body)
		}
	}
	if got := s.poll(t, adapterToken, "?wait=100ms"); len(got) != 0 {
		t.Fatalf("after ack: %+v", got)
	}
}

func (s *testServer) waitTaskSucceeded(t *testing.T, token, taskID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := s.do(t, http.MethodGet, "/api/v1/tasks/"+taskID, token, nil, nil)
		var task conloomapi.Task
		decode(t, r.body, &task)
		if r.status == http.StatusOK && task.Status == conloomapi.TaskSucceeded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not succeed: status %d, body %s", taskID, r.status, r.body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOutbox_OtherBotsActionsInvisible(t *testing.T) {
	s := newTestServer(t)
	acc := s.accept(t, adapterToken, s.event("20001", "在吗", mentionBot))
	s.waitTaskSucceeded(t, adapterToken, acc.TaskID)
	if got := s.poll(t, otherToken, "?wait=0s"); len(got) != 0 {
		t.Fatalf("other bot saw %+v", got)
	}
	actions := s.poll(t, adapterToken, "?wait=0s")
	if len(actions) != 1 {
		t.Fatalf("actions = %+v", actions)
	}
	expectError(t, s.do(t, http.MethodPost, "/api/v1/outbox/"+actions[0].ActionID+"/ack", otherToken,
		conloomapi.AckRequest{Status: conloomapi.AckSent}, nil), http.StatusNotFound, conloomapi.CodeNotFound)
}

func (s *testServer) personaState(t *testing.T, r response) conloomapi.PersonaState {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("persona: status %d, body %s", r.status, r.body)
	}
	var st conloomapi.PersonaState
	decode(t, r.body, &st)
	return st
}

func TestPersona_GetPutRollback(t *testing.T) {
	s := newTestServer(t)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	const path = "/api/v1/conversations/group_10001/persona"

	v1 := s.personaState(t, s.do(t, http.MethodGet, path, adapterToken, nil, nil))
	if v1.Version != 1 || v1.ConversationID != "group_10001" || v1.Persona != conloomapi.DefaultPersona() {
		t.Fatalf("v1 = %+v", v1)
	}

	doc := conloomapi.DefaultPersona()
	doc.PersonaName = "安静的群友"
	doc.Style.Humor = 0.1
	put := conloomapi.PersonaPut{Persona: &doc, Reason: "更安静"}
	expectError(t, s.do(t, http.MethodPut, path, adapterToken, put, nil), http.StatusForbidden, conloomapi.CodeForbidden)

	v2 := s.personaState(t, s.do(t, http.MethodPut, path, adminToken, put, nil))
	if v2.Version != 2 || v2.Persona != doc || v2.UpdatedBy != "admin-main" || v2.Reason != "更安静" {
		t.Fatalf("v2 = %+v", v2)
	}
	if got := s.personaState(t, s.do(t, http.MethodGet, path, adapterToken, nil, nil)); got.Version != 2 || got.Persona != doc {
		t.Fatalf("GET after PUT = %+v", got)
	}

	one := 1
	v3 := s.personaState(t, s.do(t, http.MethodPut, path, adminToken, conloomapi.PersonaPut{RollbackTo: &one}, nil))
	if v3.Version != 3 || v3.Persona != conloomapi.DefaultPersona() {
		t.Fatalf("v3 = %+v", v3)
	}

	bad := conloomapi.DefaultPersona()
	bad.Style.Warmth = 2
	expectError(t, s.do(t, http.MethodPut, path, adminToken, conloomapi.PersonaPut{Persona: &bad}, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, s.do(t, http.MethodPut, path, adminToken, conloomapi.PersonaPut{Reason: "空"}, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	nine := 9
	expectError(t, s.do(t, http.MethodPut, path, adminToken, conloomapi.PersonaPut{RollbackTo: &nine}, nil),
		http.StatusNotFound, conloomapi.CodeNotFound)
}

func TestPolicy_PutValidates(t *testing.T) {
	s := newTestServer(t)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	const path = "/api/v1/conversations/group_10001/policy"

	expectError(t, s.do(t, http.MethodGet, path, adapterToken, nil, nil), http.StatusForbidden, conloomapi.CodeForbidden)
	r := s.do(t, http.MethodGet, path, adminToken, nil, nil)
	var cur conloomapi.PolicyState
	decode(t, r.body, &cur)
	if r.status != http.StatusOK || cur.Version != 0 || cur.ConversationID != "group_10001" {
		t.Fatalf("GET policy: status %d, body %s", r.status, r.body)
	}

	p := cur.Policy
	p.Concurrency.MaxInteractiveRequests = 3
	expectError(t, s.do(t, http.MethodPut, path, adminToken, conloomapi.PolicyPut{Policy: p}, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	expectError(t, s.do(t, http.MethodPut, path, adapterToken, conloomapi.PolicyPut{Policy: cur.Policy}, nil),
		http.StatusForbidden, conloomapi.CodeForbidden)

	p.Concurrency.MaxInteractiveRequests = 2
	r = s.do(t, http.MethodPut, path, adminToken, conloomapi.PolicyPut{Policy: p, Reason: "并发 2"}, nil)
	var put conloomapi.PolicyState
	decode(t, r.body, &put)
	if r.status != http.StatusOK || put.Version != 1 || put.UpdatedBy != "admin-main" || put.Policy.Concurrency.MaxInteractiveRequests != 2 {
		t.Fatalf("PUT policy: status %d, body %s", r.status, r.body)
	}
	r = s.do(t, http.MethodGet, path, adminToken, nil, nil)
	decode(t, r.body, &cur)
	if cur.Version != 1 || cur.Policy.Concurrency.MaxInteractiveRequests != 2 || cur.Reason != "并发 2" {
		t.Fatalf("GET after PUT = %+v", cur)
	}
}

func TestUnknownConversation404(t *testing.T) {
	s := newTestServer(t)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	expectError(t, s.do(t, http.MethodGet, "/api/v1/conversations/group_99999/persona", adminToken, nil, nil),
		http.StatusNotFound, conloomapi.CodeNotFound)
	expectError(t, s.do(t, http.MethodGet, "/api/v1/conversations/group_99999/policy", adminToken, nil, nil),
		http.StatusNotFound, conloomapi.CodeNotFound)
	// The conversation exists for bot_main only.
	expectError(t, s.do(t, http.MethodGet, "/api/v1/conversations/group_10001/persona", otherToken, nil, nil),
		http.StatusNotFound, conloomapi.CodeNotFound)
	expectError(t, s.do(t, http.MethodGet, "/api/v1/tasks/task_missing", adapterToken, nil, nil),
		http.StatusNotFound, conloomapi.CodeNotFound)
	expectError(t, s.do(t, http.MethodPost, "/api/v1/tasks/task_missing/cancel", adapterToken, nil, nil),
		http.StatusNotFound, conloomapi.CodeNotFound)
}

func TestCapabilities(t *testing.T) {
	s := newTestServer(t)
	expectError(t, s.do(t, http.MethodGet, "/api/v1/capabilities", "", nil, nil), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	r := s.do(t, http.MethodGet, "/api/v1/capabilities", readerToken, nil, nil)
	if r.status != http.StatusOK {
		t.Fatalf("status %d, body %s", r.status, r.body)
	}
	var c conloomapi.Capabilities
	decode(t, r.body, &c)
	want := conloomapi.Capabilities{
		SchemaVersion: "1.0", BotInstanceID: "bot_main", BotName: "织语", PlatformSelfID: "30001",
		Scopes:               []string{conloomapi.ScopeOutboxRead},
		EventTypes:           []string{"message.created"},
		ActionTypes:          []string{"message.send"},
		TriggerTypes:         []string{"private", "mention", "reply", "command", "observe", "manual"},
		EnabledConversations: []conloomapi.ConversationRef{{Type: "group", PlatformID: "10001"}},
		PrivateChat:          "admins",
	}
	if c.SchemaVersion != want.SchemaVersion || c.BotInstanceID != want.BotInstanceID || c.BotName != want.BotName ||
		c.PlatformSelfID != want.PlatformSelfID || c.PrivateChat != want.PrivateChat ||
		!slices.Equal(c.Scopes, want.Scopes) || !slices.Equal(c.EventTypes, want.EventTypes) ||
		!slices.Equal(c.ActionTypes, want.ActionTypes) || !slices.Equal(c.TriggerTypes, want.TriggerTypes) ||
		!slices.Equal(c.EnabledConversations, want.EnabledConversations) {
		t.Fatalf("capabilities = %+v, want %+v", c, want)
	}
}

func TestHealthzAndMetrics(t *testing.T) {
	s := newTestServer(t)
	r := s.do(t, http.MethodGet, "/healthz", "", nil, nil)
	if r.status != http.StatusOK || string(r.body) != "ok" {
		t.Fatalf("healthz: status %d, body %q", r.status, r.body)
	}
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	r = s.do(t, http.MethodGet, "/metrics", "", nil, nil)
	if r.status != http.StatusOK || !bytes.Contains(r.body, []byte("conloom_events_total")) {
		t.Fatalf("metrics: status %d, body %s", r.status, r.body)
	}

	_ = s.st.Close()
	expectError(t, s.do(t, http.MethodGet, "/healthz", "", nil, nil), http.StatusServiceUnavailable, conloomapi.CodeInternal)
}

func TestMetricsDisabledIs404(t *testing.T) {
	s := newTestServerWith(t, serverOpts{configure: func(c *config.Config) { c.Server.MetricsEnabled = false }})
	if r := s.do(t, http.MethodGet, "/metrics", "", nil, nil); r.status != http.StatusNotFound {
		t.Fatalf("metrics disabled: status %d", r.status)
	}
}

func TestPhase2EndpointsAre404(t *testing.T) {
	s := newTestServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/memories/search"},
		{http.MethodGet, "/api/v1/tasks/task_x/events"},
		{http.MethodPost, "/api/v1/conversations/group_10001/compact"},
		{http.MethodGet, "/api/v1/stats"},
		{http.MethodGet, "/api/v1/tools"},
	} {
		if r := s.do(t, c.method, c.path, adminToken, nil, nil); r.status != http.StatusNotFound {
			t.Errorf("%s %s: status %d", c.method, c.path, r.status)
		}
	}
}

func TestPanicRecoveredAs500(t *testing.T) {
	s := newTestServerWith(t, serverOpts{metrics: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("metrics exploded")
	})})
	body := expectError(t, s.do(t, http.MethodGet, "/metrics", "", nil, nil), http.StatusInternalServerError, conloomapi.CodeInternal)
	if strings.Contains(body.Message, "exploded") {
		t.Fatalf("panic value leaked to client: %q", body.Message)
	}
	logs := s.logs.String()
	if !strings.Contains(logs, body.TraceID) || !strings.Contains(logs, "metrics exploded") {
		t.Fatalf("logs lack trace id %s or panic value:\n%s", body.TraceID, logs)
	}
}
