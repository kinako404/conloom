package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	adapterToken = "test-adapter-token"
	adminToken   = "test-admin-token"
	fakeReply    = "你好呀，我在。"
)

const testConfig = `
api_tokens:
  - {name: adapter-main, token: test-adapter-token, bot_instance_id: bot_main, scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]}
  - {name: admin-main, token: test-admin-token, bot_instance_id: bot_main, scopes: [persona:read, policy:write, admin:read]}
bots:
  - id: bot_main
    platform: qq
    name: 织语
    platform_self_id: "30001"
    admins: ["29999"]
    enabled_conversations:
      - {type: group, platform_id: "10001"}
model_profiles:
  short_context:
    endpoint: MODEL_URL
    model: short-model
    api_key: test-key-short
    context: {native_window: 3000, configured_window: 3000, soft_input_budget: 2000, reserved_output_tokens: 256, safety_margin_tokens: 64}
    runtime: {max_concurrency: 2, timeout: 5s, max_retries: 0}
    privacy: {allow_sensitive_content: true}
default_group_policy:
  concurrency: {max_interactive_requests: 1, max_queued_requests: 10, priority: normal, allow_long_requests: true}
  reply: {command_prefix: /, continuation_window: 0s, debounce: 30ms, min_reply_interval: 0s, user_rate_limit_per_minute: 6}
  context: {max_recent_messages: 50, max_context_tokens: 0}
  review: {level: basic, review_input: true, review_output: true, action: block}
`

// newGateway starts a real gateway backed by SQLite and a fake model.
func newGateway(t *testing.T) string {
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
	st, err := sqlite.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := prometheus.NewRegistry()
	metrics := obs.NewMetrics(reg)
	p := cfg.ModelProfiles["short_context"]
	ob := outbox.New(st, outbox.Config{}, metrics, nil)
	personas := persona.NewService(st, cfg.PersonaTemplates)
	policies := policy.NewService(st, cfg.DefaultGroupPolicy)
	o, err := core.New(core.Deps{
		Store:     st,
		Router:    router.New([]*router.Profile{{Role: router.RoleShort, Config: p, Client: openai.New(p, nil)}}, router.BreakerConfig{}, time.Now),
		Admission: admission.New(map[string]admission.Limits{string(router.RoleShort): {MaxConcurrency: 2}}),
		Personas:  personas,
		Policies:  policies,
		Reviewer:  review.New(cfg.Secrets(), review.Options{}),
		Notifier:  ob,
		Metrics:   metrics,
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(o.Close)
	srv := httptest.NewServer(gateway.New(gateway.Deps{
		Orchestrator: o, Outbox: ob, Personas: personas, Policies: policies, Store: st,
		Config: cfg, Metrics: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
	}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func mentionEvent(pmid, text string) conloomapi.Event {
	return conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_" + pmid,
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: "bot_main",
		Platform:      "qq",
		OccurredAt:    time.Now().UTC(),
		Conversation:  conloomapi.Conversation{Type: conloomapi.ConvGroup, ID: "group_10001", PlatformConversationID: "10001"},
		Actor:         conloomapi.Actor{ID: "user_20001", PlatformUserID: "20001", DisplayName: "用户20001"},
		Trigger:       conloomapi.Trigger{Type: conloomapi.TriggerMention, RequestedResponse: true},
		Message: conloomapi.Message{
			ID: "msg_" + pmid, PlatformMessageID: pmid, Text: text,
			Mentions: []conloomapi.Mention{{Type: "bot", ID: "bot_main"}},
		},
	}
}

func TestClient_EndToEndAndErrorMapping(t *testing.T) {
	base := newGateway(t)
	ctx := context.Background()
	c := client.New(base+"/", adapterToken, nil)

	ev := mentionEvent("1001", "在吗")
	acc, err := c.SubmitEvent(ctx, "key-1001", ev)
	if err != nil {
		t.Fatalf("SubmitEvent: %v", err)
	}
	if acc.Disposition != conloomapi.DispositionReply || acc.TaskID == "" || acc.EventID != ev.EventID {
		t.Fatalf("accepted %+v", acc)
	}
	if replay, err := c.SubmitEvent(ctx, "key-1001", ev); err != nil || replay != acc {
		t.Fatalf("replay = %+v, %v", replay, err)
	}

	actions, err := c.PollOutbox(ctx, 5, 5*time.Second)
	if err != nil {
		t.Fatalf("PollOutbox: %v", err)
	}
	if len(actions) != 1 || actions[0].Content.Text != fakeReply {
		t.Fatalf("actions = %+v", actions)
	}
	if err := c.Ack(ctx, actions[0].ActionID, conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "5001"}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if again, err := c.PollOutbox(ctx, 0, 100*time.Millisecond); err != nil || again == nil || len(again) != 0 {
		t.Fatalf("PollOutbox after ack = %#v, %v", again, err)
	}

	task, err := c.GetTask(ctx, acc.TaskID)
	if err != nil || task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("GetTask = %+v, %v", task, err)
	}
	if task, err = c.CancelTask(ctx, acc.TaskID); err != nil || task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("CancelTask = %+v, %v", task, err)
	}
	caps, err := c.Capabilities(ctx)
	if err != nil || caps.BotInstanceID != "bot_main" || caps.PlatformSelfID != "30001" {
		t.Fatalf("Capabilities = %+v, %v", caps, err)
	}

	admin := client.New(base, adminToken, &http.Client{Timeout: 10 * time.Second})
	ps, err := admin.GetPersona(ctx, "group_10001")
	if err != nil || ps.Version != 1 {
		t.Fatalf("GetPersona = %+v, %v", ps, err)
	}
	doc := conloomapi.DefaultPersona()
	doc.PersonaName = "安静的群友"
	if ps, err = admin.PutPersona(ctx, "group_10001", conloomapi.PersonaPut{Persona: &doc}); err != nil || ps.Version != 2 || ps.Persona.PersonaName != doc.PersonaName {
		t.Fatalf("PutPersona = %+v, %v", ps, err)
	}
	pol, err := admin.GetPolicy(ctx, "group_10001")
	if err != nil || pol.Version != 0 {
		t.Fatalf("GetPolicy = %+v, %v", pol, err)
	}
	pol.Policy.Concurrency.MaxInteractiveRequests = 2
	if pol, err = admin.PutPolicy(ctx, "group_10001", conloomapi.PolicyPut{Policy: pol.Policy}); err != nil || pol.Version != 1 {
		t.Fatalf("PutPolicy = %+v, %v", pol, err)
	}
	pol.Policy.Concurrency.MaxInteractiveRequests = 3
	_, err = admin.PutPolicy(ctx, "group_10001", conloomapi.PolicyPut{Policy: pol.Policy})
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Body.Code != conloomapi.CodeInvalidEvent || client.IsRetryable(err) {
		t.Fatalf("invalid policy: err = %v", err)
	}

	bad := client.New(base, "wrong-token", nil)
	_, err = bad.SubmitEvent(ctx, "key-1002", mentionEvent("1002", "在吗"))
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Body.Code != conloomapi.CodeUnauthorized || apiErr.Body.TraceID == "" {
		t.Fatalf("bad token: err = %v", err)
	}
	if client.IsRetryable(err) {
		t.Fatal("401 must not be retryable")
	}
	if _, err := c.GetTask(ctx, "task_missing"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("missing task: err = %v", err)
	}
}

func TestIsRetryable(t *testing.T) {
	statusServer := func(status int, body string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	ctx := context.Background()
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"500", 500, `{"error":{"code":"INTERNAL_ERROR","message":"internal error","retryable":false,"retry_after_ms":0,"trace_id":"trace_x"}}`, true},
		{"503 plain text", 503, "unavailable", true},
		{"429", 429, `{"error":{"code":"RATE_LIMITED","message":"slow down","retryable":false,"retry_after_ms":0,"trace_id":"trace_x"}}`, true},
		{"409 retryable body", 409, `{"error":{"code":"DUPLICATE_EVENT","message":"x","retryable":true,"retry_after_ms":0,"trace_id":"trace_x"}}`, true},
		{"404 plain text", 404, "404 page not found", false},
		{"403", 403, `{"error":{"code":"FORBIDDEN","message":"no","retryable":false,"retry_after_ms":0,"trace_id":"trace_x"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.New(statusServer(tc.status, tc.body), "t", nil).Capabilities(ctx)
			var apiErr *client.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status {
				t.Fatalf("err = %v", err)
			}
			if got := client.IsRetryable(err); got != tc.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", err, got, tc.want)
			}
		})
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := client.New(url, "t", nil).Capabilities(ctx)
	if err == nil || !client.IsRetryable(err) {
		t.Fatalf("network error: err = %v, retryable %v", err, client.IsRetryable(err))
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.New(url, "t", nil).Capabilities(cancelled); err == nil || client.IsRetryable(err) {
		t.Fatalf("cancelled context: err = %v must not be retryable", err)
	}
	if client.IsRetryable(nil) {
		t.Fatal("nil error must not be retryable")
	}
}
