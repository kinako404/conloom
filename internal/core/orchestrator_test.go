package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	testBot    = "bot_main"
	testConvID = "group_10001"
	fakeReply  = "你好呀，我在。"
)

var testConvKey = store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, testConvID)

const baseTestConfig = `
server:
  timezone: Asia/Shanghai
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
    endpoint: SHORT_URL
    model: short-model
    api_key: test-key-short
    modalities: [text]
    context: {native_window: 3000, configured_window: 3000, soft_input_budget: 2000, reserved_output_tokens: 256, safety_margin_tokens: 64}
    runtime: {max_concurrency: 2, timeout: 5s, max_retries: 0}
    privacy: {allow_sensitive_content: true}
  long_context:
    endpoint: LONG_URL
    model: long-model
    api_key: test-key-long
    modalities: [text]
    context: {native_window: 6000, configured_window: 6000, soft_input_budget: 4000, reserved_output_tokens: 256, safety_margin_tokens: 64}
    runtime: {max_concurrency: 2, timeout: 5s, max_retries: 0}
    privacy: {allow_sensitive_content: true}
default_group_policy:
  concurrency: {max_interactive_requests: 1, max_queued_requests: 10, priority: normal, allow_long_requests: true}
  reply: {command_prefix: /, continuation_window: 0s, debounce: 30ms, min_reply_interval: 0s, user_rate_limit_per_minute: 6}
  context: {max_recent_messages: 50, max_context_tokens: 0}
  review: {level: basic, review_input: true, review_output: true, action: block}
`

type handlerFunc = func(req map[string]any) (int, string)

type testOpts struct {
	short, long handlerFunc
	configure   func(*config.Config)
}

type testEnv struct {
	o        *Orchestrator
	st       *sqlite.DB
	cfg      *config.Config
	policies *policy.Service
	short    *fakeopenai.Server
	long     *fakeopenai.Server
	notifier *memNotifier
	logs     *syncBuffer
	seq      atomic.Int64
}

type memNotifier struct {
	mu    sync.Mutex
	calls map[string]int
}

func (n *memNotifier) Notify(botID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.calls == nil {
		n.calls = map[string]int{}
	}
	n.calls[botID]++
}

func (n *memNotifier) count(botID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls[botID]
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

func answer(content string) handlerFunc {
	return func(map[string]any) (int, string) { return 200, content }
}

func newTestOrchestrator(t *testing.T, opts testOpts) *testEnv {
	t.Helper()
	if opts.short == nil {
		opts.short = answer(fakeReply)
	}
	if opts.long == nil {
		opts.long = answer(fakeReply)
	}
	env := &testEnv{notifier: &memNotifier{}, logs: &syncBuffer{}}
	env.short = fakeopenai.New(t, opts.short)
	env.long = fakeopenai.New(t, opts.long)

	dir := t.TempDir()
	yml := strings.NewReplacer("SHORT_URL", env.short.URL, "LONG_URL", env.long.URL).Replace(baseTestConfig)
	path := filepath.Join(dir, "conloom.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if opts.configure != nil {
		opts.configure(cfg)
	}
	env.cfg = cfg

	st, err := sqlite.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	env.st = st

	var profiles []*router.Profile
	limits := map[string]admission.Limits{}
	for _, role := range []router.Role{router.RoleShort, router.RoleLong} {
		p, ok := cfg.ModelProfiles[string(role)]
		if !ok {
			continue
		}
		profiles = append(profiles, &router.Profile{Role: role, Config: p, Client: openai.New(p, nil)})
		limits[string(role)] = admission.Limits{MaxConcurrency: p.Runtime.MaxConcurrency}
	}
	env.policies = policy.NewService(st, cfg.DefaultGroupPolicy)
	o, err := New(Deps{
		Store:     st,
		Router:    router.New(profiles, router.BreakerConfig{}, time.Now),
		Admission: admission.New(limits),
		Personas:  persona.NewService(st, cfg.PersonaTemplates),
		Policies:  env.policies,
		Reviewer:  review.New(cfg.Secrets(), review.Options{}),
		Notifier:  env.notifier,
		Metrics:   obs.NewMetrics(prometheus.NewRegistry()),
		Logger:    slog.New(slog.NewTextHandler(env.logs, nil)),
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(o.Close)
	env.o = o
	return env
}

type evOpt func(*conloomapi.Event)

func mentionBot(ev *conloomapi.Event) {
	ev.Message.Mentions = append(ev.Message.Mentions, conloomapi.Mention{Type: "bot", ID: testBot})
	ev.Trigger = conloomapi.Trigger{Type: conloomapi.TriggerMention, RequestedResponse: true}
}

func (e *testEnv) event(convType, platformConvID, user, text string, opts ...evOpt) conloomapi.Event {
	pmid := strconv.FormatInt(1000+e.seq.Add(1), 10)
	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion,
		EventID:       "evt_" + pmid,
		EventType:     conloomapi.EventTypeMessageCreated,
		BotInstanceID: testBot,
		Platform:      "qq",
		OccurredAt:    time.Now().UTC(),
		Conversation: conloomapi.Conversation{
			Type: convType, ID: convType + "_" + platformConvID, PlatformConversationID: platformConvID,
		},
		Actor: conloomapi.Actor{
			ID: "user_" + user, PlatformUserID: user, DisplayName: "用户" + user, Roles: []string{"member"},
		},
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

func (e *testEnv) groupEvent(user, text string, opts ...evOpt) conloomapi.Event {
	return e.event(conloomapi.ConvGroup, "10001", user, text, opts...)
}

func (e *testEnv) submitRaw(t *testing.T, key string, ev conloomapi.Event) (conloomapi.EventAccepted, bool, error) {
	t.Helper()
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return e.o.Submit(context.Background(), testBot, key, body, ev)
}

func (e *testEnv) submit(t *testing.T, ev conloomapi.Event) conloomapi.EventAccepted {
	t.Helper()
	resp, replayed, err := e.submitRaw(t, "test:"+ev.Message.ID, ev)
	if err != nil {
		t.Fatalf("Submit(%s): %v", ev.Message.ID, err)
	}
	if replayed {
		t.Fatalf("Submit(%s): unexpected replay", ev.Message.ID)
	}
	return resp
}

func (e *testEnv) getTask(t *testing.T, taskID string) conloomapi.Task {
	t.Helper()
	task, err := e.o.GetTask(context.Background(), testBot, taskID)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", taskID, err)
	}
	return task
}

func (e *testEnv) waitTask(t *testing.T, taskID string) conloomapi.Task {
	t.Helper()
	var task conloomapi.Task
	waitUntil(t, "task "+taskID+" to finish", func() bool {
		task = e.getTask(t, taskID)
		return taskFinished(task.Status)
	})
	return task
}

// command submits text from user and returns the text of the command's single reply.
func (e *testEnv) command(t *testing.T, user, text string) string {
	t.Helper()
	resp := e.submit(t, e.groupEvent(user, text))
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("command %q: response %+v", text, resp)
	}
	task := e.getTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("command %q: task %+v", text, task)
	}
	return task.Actions[0].Content.Text
}

func requestMessages(t *testing.T, req map[string]any) []map[string]any {
	t.Helper()
	raw, ok := req["messages"].([]any)
	if !ok {
		t.Fatalf("request has no messages: %v", req)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestSubmit_ObserveStoresWithoutTask(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ev := env.groupEvent("20001", "大家好")
	resp := env.submit(t, ev)
	want := conloomapi.EventAccepted{EventID: ev.EventID, Disposition: conloomapi.DispositionObserve}
	if resp != want {
		t.Fatalf("response = %+v, want %+v", resp, want)
	}
	msg, err := env.st.GetMessage(context.Background(), testBot, ev.Message.ID)
	if err != nil {
		t.Fatalf("message not stored: %v", err)
	}
	if msg.Text != "大家好" || msg.ConversationKey != testConvKey || msg.PlatformUserID != "20001" {
		t.Fatalf("stored message = %+v", msg)
	}
	time.Sleep(100 * time.Millisecond)
	if n := env.short.Calls() + env.long.Calls(); n != 0 {
		t.Fatalf("model calls = %d, want 0", n)
	}
}

func TestSubmit_MentionCreatesTaskAndAction(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ev := env.groupEvent("20001", "你好", mentionBot)
	resp := env.submit(t, ev)
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" || resp.Status != conloomapi.TaskQueued {
		t.Fatalf("response = %+v", resp)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	a := task.Actions[0]
	if a.Content.Text != fakeReply {
		t.Errorf("action text = %q, want %q", a.Content.Text, fakeReply)
	}
	if a.ReplyToMessageID != ev.Message.ID || a.ReplyToPlatformMessageID != ev.Message.PlatformMessageID {
		t.Errorf("reply target = %q/%q, want %q/%q", a.ReplyToMessageID, a.ReplyToPlatformMessageID,
			ev.Message.ID, ev.Message.PlatformMessageID)
	}
	if a.Type != conloomapi.ActionMessageSend || a.ConversationID != testConvID ||
		a.ConversationType != conloomapi.ConvGroup || a.PlatformConversationID != "10001" || a.ActionID == "" {
		t.Errorf("action = %+v", a)
	}
	m := task.Meta
	if m.SelectedModelRole != "short_context" || m.SelectedModel != "short-model" ||
		m.RouteReason != "fits_short_context" || m.TraceID == "" || m.ReviewResult != review.VerdictAllow {
		t.Errorf("meta = %+v", m)
	}
	rec, err := env.st.GetOutbox(context.Background(), testBot, a.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Platform != "qq" || rec.ConversationKey != testConvKey || rec.TaskID != resp.TaskID {
		t.Errorf("outbox record = %+v", rec)
	}
	if ttl := rec.ExpiresAt.Sub(rec.CreatedAt); ttl < 9*time.Minute || ttl > 11*time.Minute {
		t.Errorf("action ttl = %v, want about 10m", ttl)
	}
	if env.notifier.count(testBot) == 0 {
		t.Error("notifier not called")
	}
}

func TestSubmit_PrivateReplyHasNoReplyTo(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	resp := env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", "在吗"))
	if resp.Disposition != conloomapi.DispositionReply {
		t.Fatalf("response = %+v", resp)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	a := task.Actions[0]
	if a.ReplyToMessageID != "" || a.ReplyToPlatformMessageID != "" {
		t.Errorf("private action has reply target %q/%q", a.ReplyToMessageID, a.ReplyToPlatformMessageID)
	}
	if a.ConversationType != conloomapi.ConvPrivate || a.PlatformConversationID != "29999" {
		t.Errorf("action = %+v", a)
	}

	other := env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗"))
	if other.Disposition != conloomapi.DispositionIgnore || other.SuppressedReason != conloomapi.SuppressedPrivateNotAllowed {
		t.Errorf("non-admin private response = %+v", other)
	}
}

func TestSubmit_ReplayReturnsSameResponse(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ev := env.groupEvent("20001", "你好", mentionBot)
	first, replayed, err := env.submitRaw(t, "k1", ev)
	if err != nil || replayed {
		t.Fatalf("first submit: replayed=%v err=%v", replayed, err)
	}
	second, replayed, err := env.submitRaw(t, "k1", ev)
	if err != nil || !replayed {
		t.Fatalf("second submit: replayed=%v err=%v", replayed, err)
	}
	if second != first || second.TaskID == "" {
		t.Fatalf("replayed response = %+v, want %+v", second, first)
	}
	env.waitTask(t, first.TaskID)
	time.Sleep(100 * time.Millisecond)
	if n := env.short.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
}

func TestSubmit_SameKeyDifferentBody(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ev := env.groupEvent("20001", "第一版")
	if _, _, err := env.submitRaw(t, "k1", ev); err != nil {
		t.Fatal(err)
	}
	ev.Message.Text = "第二版"
	_, _, err := env.submitRaw(t, "k1", ev)
	if !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("err = %v, want ErrDuplicateEvent", err)
	}
}

func TestSubmit_DuplicatePlatformMessage(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ev := env.groupEvent("20001", "同一条消息")
	if _, _, err := env.submitRaw(t, "k1", ev); err != nil {
		t.Fatal(err)
	}
	_, _, err := env.submitRaw(t, "k2", ev)
	if !errors.Is(err, store.ErrDuplicateMessage) {
		t.Fatalf("err = %v, want store.ErrDuplicateMessage", err)
	}
}

func TestSubmit_SelfMessageIgnoredNotStored(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ev := env.groupEvent("30001", "我自己说的", mentionBot)
	resp := env.submit(t, ev)
	if resp.Disposition != conloomapi.DispositionIgnore || resp.SuppressedReason != conloomapi.SuppressedSelfMessage {
		t.Fatalf("response = %+v", resp)
	}
	ctx := context.Background()
	if _, err := env.st.GetMessage(ctx, testBot, ev.Message.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessage err = %v, want ErrNotFound", err)
	}
	rec, err := env.st.GetEvent(ctx, testBot, "test:"+ev.Message.ID)
	if err != nil {
		t.Fatalf("idempotency record missing: %v", err)
	}
	if len(rec.RawEvent) != 0 {
		t.Errorf("ignored event stored its raw body: %s", rec.RawEvent)
	}
	again, replayed, err := env.submitRaw(t, "test:"+ev.Message.ID, ev)
	if err != nil || !replayed || again != resp {
		t.Fatalf("replay = %+v, %v, %v", again, replayed, err)
	}
}

func TestSubmit_NotEnabledGroupNotStored(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.Bots[0].EnabledConversations = nil
	}})
	ev := env.groupEvent("20001", "你好", mentionBot)
	resp := env.submit(t, ev)
	if resp.Disposition != conloomapi.DispositionIgnore || resp.SuppressedReason != conloomapi.SuppressedNotEnabled {
		t.Fatalf("response = %+v", resp)
	}
	ctx := context.Background()
	if _, err := env.st.GetMessage(ctx, testBot, ev.Message.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessage err = %v, want ErrNotFound", err)
	}
	if _, err := env.st.GetConversation(ctx, testBot, testConvID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetConversation err = %v, want ErrNotFound", err)
	}
}

func TestSubmit_FollowUpWithinDebounceMerged(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.Debounce = conloomapi.Duration{Duration: 500 * time.Millisecond}
	}})
	first := env.submit(t, env.groupEvent("20001", "第一段", mentionBot))
	second := env.submit(t, env.groupEvent("20001", "第二段"))
	if second.Disposition != conloomapi.DispositionReply || !second.Merged || second.TaskID != first.TaskID {
		t.Fatalf("follow-up response = %+v (first %+v)", second, first)
	}
	task := env.waitTask(t, first.TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	time.Sleep(100 * time.Millisecond)
	if n := env.short.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
	msgs := requestMessages(t, env.short.Requests()[0])
	last := msgs[len(msgs)-1]
	content, _ := last["content"].(string)
	if last["role"] != "user" || !strings.Contains(content, "第一段") || !strings.Contains(content, "第二段") {
		t.Fatalf("last message = %v", last)
	}
	rec, err := env.st.GetTask(context.Background(), testBot, first.TaskID)
	if err != nil || len(rec.TriggerMessageIDs) != 2 {
		t.Fatalf("stored triggers = %v, %v", rec.TriggerMessageIDs, err)
	}
}

func TestSubmit_RateLimitedObserves(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	for i := range 6 {
		resp := env.submit(t, env.groupEvent("20001", "第"+strconv.Itoa(i)+"次", mentionBot))
		if resp.Disposition != conloomapi.DispositionReply {
			t.Fatalf("mention %d: response %+v", i+1, resp)
		}
	}
	ev := env.groupEvent("20001", "第七次", mentionBot)
	resp := env.submit(t, ev)
	if resp.Disposition != conloomapi.DispositionObserve || resp.SuppressedReason != conloomapi.SuppressedRateLimited || resp.TaskID != "" {
		t.Fatalf("7th mention: response %+v", resp)
	}
	if _, err := env.st.GetMessage(context.Background(), testBot, ev.Message.ID); err != nil {
		t.Fatalf("rate-limited message not stored: %v", err)
	}
	other := env.submit(t, env.groupEvent("20002", "我也来", mentionBot))
	if other.Disposition != conloomapi.DispositionReply {
		t.Fatalf("other user's mention: response %+v", other)
	}
}

func TestSubmit_PausedObserves(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Concurrency.MaxInteractiveRequests = 0
	}})
	ev := env.groupEvent("20001", "你好", mentionBot)
	resp := env.submit(t, ev)
	if resp.Disposition != conloomapi.DispositionObserve || resp.SuppressedReason != conloomapi.SuppressedPaused || resp.TaskID != "" {
		t.Fatalf("response = %+v", resp)
	}
	if _, err := env.st.GetMessage(context.Background(), testBot, ev.Message.ID); err != nil {
		t.Fatalf("paused message not stored: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := env.short.Calls(); n != 0 {
		t.Fatalf("model calls = %d, want 0", n)
	}
}

func TestSubmit_QueueFullObserves(t *testing.T) {
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{
		short: func(map[string]any) (int, string) { <-release; return 200, fakeReply },
		configure: func(c *config.Config) {
			c.DefaultGroupPolicy.Concurrency.MaxQueuedRequests = 1
		},
	})
	t.Cleanup(func() { close(release) })
	env.submit(t, env.groupEvent("20001", "一", mentionBot))
	waitUntil(t, "first task running", func() bool { return env.short.Calls() == 1 })
	if resp := env.submit(t, env.groupEvent("20002", "二", mentionBot)); resp.Disposition != conloomapi.DispositionReply {
		t.Fatalf("second mention: response %+v", resp)
	}
	ev := env.groupEvent("20001", "三", mentionBot)
	resp := env.submit(t, ev)
	if resp.Disposition != conloomapi.DispositionObserve || resp.SuppressedReason != conloomapi.SuppressedQueueFull {
		t.Fatalf("third mention: response %+v", resp)
	}
	if _, err := env.st.GetMessage(context.Background(), testBot, ev.Message.ID); err != nil {
		t.Fatalf("queue_full message not stored: %v", err)
	}
}

func TestCommand_Help(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	if got := env.command(t, "20001", "/help"); got != CopyHelp("织语") {
		t.Fatalf("help = %q", got)
	}
	if n := env.short.Calls(); n != 0 {
		t.Fatalf("model calls = %d, want 0", n)
	}
}

func TestCommand_Persona(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	if got, want := env.command(t, "20001", "/persona"), CopyPersonaInfo("默认群友人格", 1); got != want {
		t.Fatalf("persona = %q, want %q", got, want)
	}
}

func TestCommand_PauseRequiresAdmin(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	concurrency := func() int {
		pv, err := env.policies.Current(context.Background(), testConvKey)
		if err != nil {
			t.Fatal(err)
		}
		return pv.Policy.Concurrency.MaxInteractiveRequests
	}
	if got := env.command(t, "20001", "/pause"); got != CopyAdminOnly {
		t.Fatalf("non-admin pause = %q", got)
	}
	if c := concurrency(); c != 1 {
		t.Fatalf("concurrency after non-admin pause = %d", c)
	}
	if got := env.command(t, "29999", "/pause"); got != CopyPaused {
		t.Fatalf("admin pause = %q", got)
	}
	if c := concurrency(); c != 0 {
		t.Fatalf("concurrency after pause = %d", c)
	}
	if resp := env.submit(t, env.groupEvent("20001", "在吗", mentionBot)); resp.SuppressedReason != conloomapi.SuppressedPaused {
		t.Fatalf("mention while paused: response %+v", resp)
	}
	if got := env.command(t, "20001", "/resume"); got != CopyAdminOnly {
		t.Fatalf("non-admin resume = %q", got)
	}
	if got := env.command(t, "29999", "/resume"); got != CopyResumed {
		t.Fatalf("admin resume = %q", got)
	}
	if c := concurrency(); c != 1 {
		t.Fatalf("concurrency after resume = %d", c)
	}
}

func TestCommand_StopCancelsOwnRunningTask(t *testing.T) {
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) { <-release; return 200, fakeReply }})
	t.Cleanup(func() { close(release) })
	resp := env.submit(t, env.groupEvent("20001", "讲个长故事", mentionBot))
	waitUntil(t, "model called", func() bool { return env.short.Calls() == 1 })
	if got := env.command(t, "20001", "/stop"); got != CopyStopDone {
		t.Fatalf("stop = %q", got)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskCancelled {
		t.Fatalf("task status = %q", task.Status)
	}
	if task.Actions == nil || len(task.Actions) != 0 {
		t.Fatalf("actions = %#v, want empty non-nil", task.Actions)
	}
	if task.Error == nil || task.Error.Code != conloomapi.CodeTaskCancelled {
		t.Fatalf("error = %+v", task.Error)
	}
	if got := env.command(t, "20001", "/stop"); got != CopyStopNothing {
		t.Fatalf("second stop = %q", got)
	}
}

func TestCommand_StopOthersForbidden(t *testing.T) {
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) { <-release; return 200, fakeReply }})
	t.Cleanup(func() { close(release) })
	resp := env.submit(t, env.groupEvent("20001", "讲个长故事", mentionBot))
	waitUntil(t, "model called", func() bool { return env.short.Calls() == 1 })
	if got := env.command(t, "20002", "/stop"); got != CopyStopForbidden {
		t.Fatalf("stop by other user = %q", got)
	}
	if task := env.getTask(t, resp.TaskID); task.Status != conloomapi.TaskRunning {
		t.Fatalf("task status after forbidden stop = %q", task.Status)
	}
	if got := env.command(t, "29999", "/stop"); got != CopyStopDone {
		t.Fatalf("stop by admin = %q", got)
	}
	if task := env.waitTask(t, resp.TaskID); task.Status != conloomapi.TaskCancelled {
		t.Fatalf("task status after admin stop = %q", task.Status)
	}
}

func TestCancelTask_RunningAndUnknown(t *testing.T) {
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) { <-release; return 200, fakeReply }})
	t.Cleanup(func() { close(release) })
	resp := env.submit(t, env.groupEvent("20001", "你好", mentionBot))
	waitUntil(t, "model called", func() bool { return env.short.Calls() == 1 })
	ctx := context.Background()
	if _, err := env.o.CancelTask(ctx, "bot_other", resp.TaskID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancel via other bot: err = %v, want ErrNotFound", err)
	}
	task, err := env.o.CancelTask(ctx, testBot, resp.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != conloomapi.TaskCancelled || len(task.Actions) != 0 {
		t.Fatalf("cancelled task = %+v", task)
	}
	again, err := env.o.CancelTask(ctx, testBot, resp.TaskID)
	if err != nil || again.Status != conloomapi.TaskCancelled {
		t.Fatalf("second cancel = %+v, %v", again, err)
	}
}

func TestGetTask_ActionsEncodeAsEmptyArray(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.Debounce = conloomapi.Duration{Duration: time.Minute}
	}})
	resp := env.submit(t, env.groupEvent("20001", "你好", mentionBot))
	task := env.getTask(t, resp.TaskID)
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"actions":[]`) {
		t.Fatalf("task JSON = %s", b)
	}
}

func TestNew_WarnsUnimplementedReviewLevel(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Review.Level = "strict"
	}})
	logs := env.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "strict") {
		t.Fatalf("logs = %q", logs)
	}
}

func TestSubmit_LogsOmitMessageText(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	resp := env.submit(t, env.groupEvent("20001", "这是一句私密内容", mentionBot))
	env.waitTask(t, resp.TaskID)
	if logs := env.logs.String(); strings.Contains(logs, "私密内容") || strings.Contains(logs, fakeReply) {
		t.Fatalf("logs contain message text: %q", logs)
	}
}
