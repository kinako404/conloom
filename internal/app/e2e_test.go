package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	botID        = "bot_main"
	adapterToken = "fake-adapter-token"
	fakeReply    = "你好呀，我在。"
	drainWait    = 10 * time.Second
)

var (
	group = mock.Conv{Type: conloomapi.ConvGroup, PlatformID: "10001"}
	alice = mock.User{PlatformID: "20001", Name: "小明"}
	bob   = mock.User{PlatformID: "20002", Name: "小红"}
	admin = mock.User{PlatformID: "29999", Name: "管理员"}
	self  = mock.User{PlatformID: "30001", Name: "织语"}
)

const configTemplate = `
server:
  listen: 127.0.0.1:0
  timezone: Asia/Shanghai
  metrics_enabled: true
storage:
  data_dir: DATA_DIR
api_tokens:
  - name: adapter
    token: fake-adapter-token
    bot_instance_id: bot_main
    scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]
bots:
  - id: bot_main
    platform: qq
    name: 织语
    platform_self_id: "30001"
    admins: ["29999"]
    enabled_conversations:
      - {type: group, platform_id: "10001"}
    private_chat: admins
model_profiles:
  short_context:
    endpoint: MODEL_URL
    model: MODEL_NAME
    api_key: fake-model-key
    modalities: [text]
    context: {native_window: 32768, configured_window: 32768, soft_input_budget: 16000, reserved_output_tokens: 1024, safety_margin_tokens: 1024}
    runtime: {max_concurrency: 2, timeout: 10s, max_retries: 0}
    privacy: {data_location: remote, allow_sensitive_content: true}
default_group_policy:
  concurrency: {max_interactive_requests: 1, max_queued_requests: 10, priority: normal, allow_long_requests: true}
  reply: {command_prefix: /, continuation_window: CONTINUATION, debounce: DEBOUNCE, min_reply_interval: 0s, user_rate_limit_per_minute: RATE_LIMIT}
  context: {max_recent_messages: 50, max_context_tokens: 0}
  review: {level: basic, review_input: true, review_output: true, action: block}
  proactive: {user_cooldown: COOLDOWN, contact_cooldown: CONTACT}
runtime:
  outbox_lease: LEASE
`

type configParams struct {
	dataDir   string
	modelURL  string
	modelName string
	lease     string
	debounce  string
	rateLimit int
	// continuation is the continuation window; empty means 0s.
	continuation string
	// cooldown is proactive.user_cooldown; empty means the default 30m.
	cooldown string
	// contact is proactive.contact_cooldown; empty means the default 0s.
	contact string
}

// testConfig writes a config for p to a temporary directory and loads it.
func testConfig(t testing.TB, p configParams) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if p.dataDir == "" {
		p.dataDir = filepath.Join(dir, "data")
	}
	if p.modelName == "" {
		p.modelName = "fake-model"
	}
	if p.lease == "" {
		p.lease = "60s"
	}
	if p.debounce == "" {
		p.debounce = "20ms"
	}
	if p.rateLimit == 0 {
		p.rateLimit = 6
	}
	if p.continuation == "" {
		p.continuation = "0s"
	}
	if p.cooldown == "" {
		p.cooldown = "30m"
	}
	if p.contact == "" {
		p.contact = "0s"
	}
	yml := strings.NewReplacer(
		"DATA_DIR", strconv.Quote(p.dataDir),
		"MODEL_URL", p.modelURL,
		"MODEL_NAME", p.modelName,
		"LEASE", p.lease,
		"DEBOUNCE", p.debounce,
		"RATE_LIMIT", strconv.Itoa(p.rateLimit),
		"CONTINUATION", p.continuation,
		"COOLDOWN", p.cooldown,
		"CONTACT", p.contact,
	).Replace(configTemplate)
	path := filepath.Join(dir, "conloom.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	return cfg
}

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

type env struct {
	t      testing.TB
	app    *app.App
	srv    *httptest.Server
	client *client.Client
	mock   *mock.Mock
}

// newEnv starts an app for cfg behind an httptest server. It is closed when
// the test ends; close may also be called earlier.
func newEnv(t testing.TB, cfg *config.Config) *env {
	t.Helper()
	return newEnvWith(t, cfg, app.Options{})
}

// newEnvWith is newEnv with app options.
func newEnvWith(t testing.TB, cfg *config.Config, opts app.Options) *env {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	if _, bench := t.(*testing.B); !bench {
		logs := &syncBuffer{}
		logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("app logs:\n%s", logs)
			}
		})
	}
	a, err := app.New(cfg, logger, opts)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if err := a.Start(context.Background()); err != nil {
		_ = a.Close()
		t.Fatalf("Start: %v", err)
	}
	e := &env{t: t, app: a, srv: httptest.NewServer(a.Handler())}
	t.Cleanup(e.close)
	e.client = client.New(e.srv.URL, adapterToken, nil)
	e.mock = mock.New(e.client, botID, "qq")
	return e
}

func (e *env) close() {
	e.srv.Close()
	if err := e.app.Close(); err != nil {
		e.t.Errorf("app.Close: %v", err)
	}
}

func (e *env) say(conv mock.Conv, user mock.User, text string, o mock.SayOptions) (conloomapi.EventAccepted, string) {
	e.t.Helper()
	resp, msgID, err := e.mock.Say(context.Background(), conv, user, text, o)
	if err != nil {
		e.t.Fatalf("Say(%q): %v", text, err)
	}
	return resp, msgID
}

// mention sends text mentioning the bot and requires a reply task.
func (e *env) mention(conv mock.Conv, user mock.User, text string) string {
	e.t.Helper()
	resp, msgID := e.say(conv, user, text, mock.SayOptions{MentionBot: true})
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		e.t.Fatalf("mention %q: response %+v, want a reply task", text, resp)
	}
	return msgID
}

func (e *env) drain(wait time.Duration) []conloomapi.Action {
	e.t.Helper()
	actions, err := e.mock.Drain(context.Background(), wait)
	if err != nil {
		e.t.Fatalf("Drain: %v", err)
	}
	return actions
}

// drainOne drains until an action arrives and requires exactly one.
func (e *env) drainOne() conloomapi.Action {
	e.t.Helper()
	actions := e.drain(drainWait)
	if len(actions) != 1 {
		e.t.Fatalf("drained %d actions, want 1: %+v", len(actions), actions)
	}
	return actions[0]
}

func (e *env) requireNoAction(wait time.Duration) {
	e.t.Helper()
	if actions := e.drain(wait); len(actions) != 0 {
		e.t.Fatalf("drained unexpected actions: %+v", actions)
	}
}

func answer(content string) func(map[string]any) (int, string) {
	return func(map[string]any) (int, string) { return 200, content }
}

// blockingModel returns a fake model that answers only after the test ends.
func blockingModel(t *testing.T) *fakeopenai.Server {
	release := make(chan struct{})
	fake := fakeopenai.New(t, func(map[string]any) (int, string) {
		<-release
		return 200, "迟到的回复"
	})
	t.Cleanup(func() { close(release) })
	return fake
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func requestText(req map[string]any) string {
	var sb strings.Builder
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			c, _ := mm["content"].(string)
			sb.WriteString(c)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// groupMentionEvent is a message from alice in group mentioning the bot, with
// platform message ID pmid.
func groupMentionEvent(pmid, text string, occurredAt time.Time) conloomapi.Event {
	return conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_" + pmid,
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: botID,
		Platform:      "qq",
		OccurredAt:    occurredAt.UTC(),
		Conversation:  conloomapi.Conversation{Type: conloomapi.ConvGroup, ID: "group_10001", PlatformConversationID: "10001"},
		Actor:         conloomapi.Actor{ID: "user_20001", PlatformUserID: "20001", DisplayName: "小明", Roles: []string{"member"}},
		Trigger:       conloomapi.Trigger{Type: conloomapi.TriggerMention, RequestedResponse: true},
		Message: conloomapi.Message{
			ID: "msg_" + pmid, PlatformMessageID: pmid, Text: text,
			Mentions:    []conloomapi.Mention{{Type: "bot", ID: botID}},
			Attachments: []conloomapi.Attachment{},
		},
	}
}

func TestE2E_GroupMentionGetsOneReply(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL}))

	msgID := e.mention(group, alice, "在吗？")
	a := e.drainOne()
	if a.Content.Text != fakeReply {
		t.Errorf("reply text = %q, want %q", a.Content.Text, fakeReply)
	}
	if a.ReplyToMessageID != msgID {
		t.Errorf("ReplyToMessageID = %q, want %q", a.ReplyToMessageID, msgID)
	}
	if a.ConversationID != "group_10001" || a.PlatformConversationID != "10001" || a.Type != conloomapi.ActionMessageSend {
		t.Errorf("action target = %+v", a)
	}
	e.requireNoAction(300 * time.Millisecond)
	if n := fake.Calls(); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
}

func TestE2E_OwnMessagesAreIgnored(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL}))

	msgID := e.mention(group, alice, "在吗？")
	e.drainOne()

	resp, _ := e.say(group, self, fakeReply, mock.SayOptions{MentionBot: true, ReplyTo: msgID})
	if resp.Disposition != conloomapi.DispositionIgnore || resp.SuppressedReason != conloomapi.SuppressedSelfMessage || resp.TaskID != "" {
		t.Fatalf("message from the bot itself: response %+v, want ignore/self_message", resp)
	}
	e.requireNoAction(500 * time.Millisecond)
	if n := fake.Calls(); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
}

func TestE2E_ReplayedEventsAreNotAnsweredTwice(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL}))
	ctx := context.Background()

	ev := groupMentionEvent("900002", "重复投递的问题", time.Now())
	first, err := e.client.SubmitEvent(ctx, "test:900002", ev)
	if err != nil || first.Disposition != conloomapi.DispositionReply || first.TaskID == "" {
		t.Fatalf("SubmitEvent = %+v, %v; want a reply task", first, err)
	}
	e.drainOne()

	again, err := e.client.SubmitEvent(ctx, "test:900002", ev)
	if err != nil || again.TaskID != first.TaskID || again.Disposition != first.Disposition {
		t.Errorf("SubmitEvent with the same key = %+v, %v; want the first response %+v", again, err, first)
	}

	_, err = e.client.SubmitEvent(ctx, "test:900002-retry", ev)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Body.Code != conloomapi.CodeDuplicateEvent {
		t.Errorf("SubmitEvent of the same message with a new key: err = %v, want 409 %s", err, conloomapi.CodeDuplicateEvent)
	}

	e.requireNoAction(500 * time.Millisecond)
	if n := fake.Calls(); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
	task, err := e.client.GetTask(ctx, first.TaskID)
	if err != nil || len(task.Actions) != 1 {
		t.Errorf("GetTask = %+v, %v; want exactly 1 action", task, err)
	}
}

func TestE2E_ObservedMessagesDoNotCallModel(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL}))

	var texts []string
	for i := range 20 {
		user := alice
		if i%2 == 1 {
			user = bob
		}
		text := "普通聊天第" + strconv.Itoa(i+1) + "条"
		texts = append(texts, text)
		resp, _ := e.say(group, user, text, mock.SayOptions{})
		if resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
			t.Fatalf("message %d: response %+v, want observe", i+1, resp)
		}
	}
	if n := fake.Calls(); n != 0 {
		t.Fatalf("model calls after observed messages = %d, want 0", n)
	}

	e.mention(group, alice, "大家刚才在聊什么？")
	e.drainOne()
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(reqs))
	}
	prompt := requestText(reqs[0])
	for _, text := range texts {
		if !strings.Contains(prompt, text) {
			t.Errorf("model request does not contain observed message %q", text)
		}
	}
}

func TestE2E_ReplyToBotTriggers(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL}))

	e.mention(group, alice, "在吗？")
	e.drainOne() // acked as platform message "mockbot-1"

	resp, msgID := e.say(group, bob, "那你接着说说", mock.SayOptions{ReplyTo: "msg_mockbot-1"})
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("reply to bot: response %+v, want a reply task", resp)
	}
	a := e.drainOne()
	if a.ReplyToMessageID != msgID || a.Content.Text != fakeReply {
		t.Errorf("action = %+v, want reply to %s", a, msgID)
	}

	resp, _ = e.say(group, bob, "回复别人的消息", mock.SayOptions{ReplyTo: msgID})
	if resp.Disposition != conloomapi.DispositionObserve {
		t.Errorf("reply to a user message: response %+v, want observe", resp)
	}
	if n := fake.Calls(); n != 2 {
		t.Errorf("model calls = %d, want 2", n)
	}
}

func TestE2E_LeaseExpiryRedeliversButAckedNeverAgain(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL, lease: "200ms"}))
	ctx := context.Background()

	e.mention(group, alice, "在吗？")
	first, err := e.client.PollOutbox(ctx, 0, drainWait)
	if err != nil || len(first) != 1 {
		t.Fatalf("PollOutbox = %+v, %v; want 1 action", first, err)
	}
	if again, err := e.client.PollOutbox(ctx, 0, 0); err != nil || len(again) != 0 {
		t.Fatalf("PollOutbox while leased = %+v, %v; want none", again, err)
	}

	time.Sleep(300 * time.Millisecond)
	redelivered, err := e.client.PollOutbox(ctx, 0, 0)
	if err != nil || len(redelivered) != 1 || redelivered[0].ActionID != first[0].ActionID {
		t.Fatalf("PollOutbox after lease expiry = %+v, %v; want action %s again", redelivered, err, first[0].ActionID)
	}

	if err := e.client.Ack(ctx, first[0].ActionID, conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "mockbot-77"}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	start := time.Now()
	after, err := e.client.PollOutbox(ctx, 0, time.Second)
	if err != nil || len(after) != 0 {
		t.Fatalf("PollOutbox after ack = %+v, %v; want none", after, err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("PollOutbox returned after %v, want it to wait about 1s", elapsed)
	}
}

func TestE2E_RestartRecovery(t *testing.T) {
	dataDir := t.TempDir()
	slow := blockingModel(t)
	e1 := newEnv(t, testConfig(t, configParams{dataDir: dataDir, modelURL: slow.URL}))
	msgID := e1.mention(group, alice, "在吗？")
	waitUntil(t, "the model call to start", func() bool { return slow.Calls() == 1 })
	e1.close()

	fast := fakeopenai.New(t, answer(fakeReply))
	e2 := newEnv(t, testConfig(t, configParams{dataDir: dataDir, modelURL: fast.URL}))
	a := e2.drainOne()
	if a.Content.Text != fakeReply || a.ReplyToMessageID != msgID {
		t.Errorf("recovered action = %+v, want %q replying to %s", a, fakeReply, msgID)
	}
	e2.requireNoAction(500 * time.Millisecond)
	if n := fast.Calls(); n != 1 {
		t.Errorf("model calls after restart = %d, want 1", n)
	}
}

func TestE2E_StaleTaskNotAnsweredAfterRestart(t *testing.T) {
	dataDir := t.TempDir()
	slow := blockingModel(t)
	e1 := newEnv(t, testConfig(t, configParams{dataDir: dataDir, modelURL: slow.URL}))
	ev := groupMentionEvent("900001", "停机前的问题", time.Now())
	resp, err := e1.client.SubmitEvent(context.Background(), "test:900001", ev)
	if err != nil || resp.TaskID == "" {
		t.Fatalf("SubmitEvent = %+v, %v; want a reply task", resp, err)
	}
	waitUntil(t, "the model call to start", func() bool { return slow.Calls() == 1 })
	e1.close()

	// The core comes back five minutes later.
	fast := fakeopenai.New(t, answer(fakeReply))
	later := func() time.Time { return time.Now().Add(5 * time.Minute) }
	e2 := newEnvWith(t, testConfig(t, configParams{dataDir: dataDir, modelURL: fast.URL}), app.Options{Now: later})
	e2.requireNoAction(time.Second)
	task, err := e2.client.GetTask(context.Background(), resp.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != conloomapi.TaskFailed || task.Error == nil || task.Error.Code != "STALE" || len(task.Actions) != 0 {
		t.Errorf("stale task = %+v (error %+v), want failed STALE without actions", task, task.Error)
	}
	if n := fast.Calls(); n != 0 {
		t.Errorf("model calls after restart = %d, want 0", n)
	}
}

func TestE2E_OldMentionSubmittedLateIsNotAnswered(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL}))
	ctx := context.Background()

	ev := groupMentionEvent("900003", "五分钟前的问题", time.Now().Add(-5*time.Minute))
	resp, err := e.client.SubmitEvent(ctx, "test:900003", ev)
	if err != nil {
		t.Fatalf("SubmitEvent: %v", err)
	}
	if resp.Disposition != conloomapi.DispositionObserve || resp.SuppressedReason != conloomapi.SuppressedStale || resp.TaskID != "" {
		t.Fatalf("old mention submitted late: response %+v, want observe/%s", resp, conloomapi.SuppressedStale)
	}
	e.requireNoAction(500 * time.Millisecond)
	if n := fake.Calls(); n != 0 {
		t.Fatalf("model calls = %d, want 0", n)
	}

	e.mention(group, bob, "刚才有人问了什么？")
	e.drainOne()
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(reqs))
	}
	if !strings.Contains(requestText(reqs[0]), "五分钟前的问题") {
		t.Error("the stale mention was not stored as context")
	}
}

func TestE2E_ModelSwapByConfigOnly(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	for i, model := range []string{"fake-model-alpha", "fake-model-beta"} {
		e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL, modelName: model}))
		e.mention(group, alice, "在吗？")
		e.drainOne()
		reqs := fake.Requests()
		if len(reqs) != i+1 {
			t.Fatalf("model calls = %d, want %d", len(reqs), i+1)
		}
		if got := reqs[i]["model"]; got != model {
			t.Errorf("request model = %v, want %q", got, model)
		}
	}
}

func TestE2E_PrivateChatAdminOnly(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL}))

	resp, _ := e.say(mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: alice.PlatformID}, alice, "你好", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionIgnore || resp.SuppressedReason != conloomapi.SuppressedPrivateNotAllowed {
		t.Fatalf("private message from a non-admin: response %+v, want ignore/private_not_allowed", resp)
	}

	resp, _ = e.say(mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: admin.PlatformID}, admin, "你好", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("private message from the admin: response %+v, want a reply task", resp)
	}
	a := e.drainOne()
	if a.ConversationID != "private_29999" || a.ConversationType != conloomapi.ConvPrivate ||
		a.PlatformConversationID != "29999" || a.ReplyToMessageID != "" || a.Content.Text != fakeReply {
		t.Errorf("private reply = %+v", a)
	}
	if n := fake.Calls(); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
}

// proactiveDirective is the start of the hint appended to proactive requests.
const proactiveDirective = "【提示】没有人 @ 你"

func TestE2E_ProactiveReplyForEligibleUser(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL, cooldown: "0s"}))

	e.mention(group, alice, "我在研究咖啡豆烘焙")
	e.drainOne()
	resp, msgID := e.say(group, alice, "咖啡豆烘焙真难", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("matching message from an eligible user: response %+v, want a proactive task", resp)
	}
	a := e.drainOne()
	if a.Content.Text != fakeReply || a.ReplyToMessageID != msgID {
		t.Errorf("proactive action = %+v, want %q quoting %s", a, fakeReply, msgID)
	}
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(reqs))
	}
	if text := requestText(reqs[1]); !strings.Contains(text, proactiveDirective) || !strings.Contains(text, "咖啡") {
		t.Errorf("proactive request lacks the directive with interest terms: %q", text)
	}
	task, err := e.client.GetTask(context.Background(), resp.TaskID)
	if err != nil || task.Meta.Trigger != conloomapi.TriggerProactive {
		t.Errorf("GetTask = %+v, %v; want trigger proactive", task, err)
	}

	if got := e.commandReply(alice, "/主动 关"); !strings.Contains(got, "关") {
		t.Fatalf("/主动 关 reply = %q", got)
	}
	resp, _ = e.say(group, alice, "咖啡豆烘焙还要练", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
		t.Fatalf("matching message after /主动 关: response %+v, want observe", resp)
	}
	e.requireNoAction(300 * time.Millisecond)
	if n := fake.Calls(); n != 2 {
		t.Errorf("model calls = %d, want 2", n)
	}
}

// commandReply sends a command from user and returns the text of its reply.
func (e *env) commandReply(user mock.User, text string) string {
	e.t.Helper()
	resp, _ := e.say(group, user, text, mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		e.t.Fatalf("command %q: response %+v", text, resp)
	}
	return e.drainOne().Content.Text
}

func TestE2E_NeverMentionedUserGetsNoProactive(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL}))

	e.mention(group, alice, "我在研究咖啡豆烘焙")
	e.drainOne()
	for i := range 10 {
		resp, _ := e.say(group, bob, "我在研究咖啡豆烘焙"+strconv.Itoa(i), mock.SayOptions{})
		if resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
			t.Fatalf("message %d from a user who never mentioned the bot: response %+v, want observe", i, resp)
		}
	}
	e.requireNoAction(300 * time.Millisecond)
	if n := fake.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1 (only the mention)", n)
	}
}

func TestE2E_ContinuationExitWhenTalkingToOthers(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL, continuation: "60s"}))

	e.mention(group, alice, "在吗？")
	e.drainOne()
	_, bobMsg := e.say(group, bob, "小明你周末去哪", mock.SayOptions{})
	resp, _ := e.say(group, alice, "去爬山啊", mock.SayOptions{ReplyTo: bobMsg})
	if resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
		t.Fatalf("reply to another member in continuation: response %+v, want observe", resp)
	}
	resp, _ = e.say(group, alice, "下午出发", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
		t.Fatalf("message after the continuation ended: response %+v, want observe", resp)
	}

	e.mention(group, alice, "我回来了")
	e.drainOne()
	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_900010",
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: botID,
		Platform:      "qq",
		OccurredAt:    time.Now().UTC(),
		Conversation:  conloomapi.Conversation{Type: conloomapi.ConvGroup, ID: "group_10001", PlatformConversationID: "10001"},
		Actor:         conloomapi.Actor{ID: "user_20001", PlatformUserID: "20001", DisplayName: "小明", Roles: []string{"member"}},
		Trigger:       conloomapi.Trigger{Type: conloomapi.TriggerObserve},
		Message: conloomapi.Message{
			ID: "msg_900010", PlatformMessageID: "900010", Text: "小红快来看",
			Mentions:    []conloomapi.Mention{{Type: "user", ID: "user_20002"}},
			Attachments: []conloomapi.Attachment{},
		},
	}
	resp, err := e.client.SubmitEvent(context.Background(), "test:900010", ev)
	if err != nil || resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
		t.Fatalf("mention of another member in continuation: response %+v, %v; want observe", resp, err)
	}
	resp, _ = e.say(group, alice, "晚上一起吃饭", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
		t.Fatalf("message after the continuation ended: response %+v, want observe", resp)
	}
	e.requireNoAction(300 * time.Millisecond)
	if n := fake.Calls(); n != 2 {
		t.Fatalf("model calls = %d, want 2 (one per mention)", n)
	}
}

func TestE2E_NoProactiveRightAfterReplyWithContactCooldown(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL, contact: "30m"}))

	e.mention(group, alice, "我在研究咖啡豆烘焙")
	e.drainOne()
	resp, _ := e.say(group, alice, "咖啡豆烘焙真难", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
		t.Fatalf("on-topic message right after a reply: response %+v, want observe", resp)
	}
	e.requireNoAction(300 * time.Millisecond)
	if n := fake.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1 (only the mention)", n)
	}
}

func TestE2E_ProactiveShortlyAfterConversationByDefault(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, testConfig(t, configParams{modelURL: fake.URL, continuation: "1s"}))

	e.mention(group, alice, "我在研究咖啡豆烘焙")
	e.drainOne()
	time.Sleep(1200 * time.Millisecond)
	resp, msgID := e.say(group, alice, "咖啡豆烘焙真难", mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("on-topic message after the continuation window: response %+v, want a proactive task", resp)
	}
	a := e.drainOne()
	if a.Content.Text != fakeReply || a.ReplyToMessageID != msgID {
		t.Errorf("proactive action = %+v, want %q quoting %s", a, fakeReply, msgID)
	}
	task, err := e.client.GetTask(context.Background(), resp.TaskID)
	if err != nil || task.Meta.Trigger != conloomapi.TriggerProactive {
		t.Errorf("GetTask = %+v, %v; want trigger proactive", task, err)
	}
	if n := fake.Calls(); n != 2 {
		t.Errorf("model calls = %d, want 2", n)
	}
}
