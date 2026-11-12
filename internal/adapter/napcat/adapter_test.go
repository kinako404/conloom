package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	testCoreToken = "fake-adapter-token"
	fakeReply     = "你好呀，我在。"
)

const coreConfig = `
server:
  listen: 127.0.0.1:0
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
    model: fake-model
    api_key: fake-model-key
    modalities: [text]
    context: {native_window: 32768, configured_window: 32768, soft_input_budget: 16000, reserved_output_tokens: 1024, safety_margin_tokens: 1024}
    runtime: {max_concurrency: 2, timeout: 10s, max_retries: 0}
    privacy: {data_location: remote, allow_sensitive_content: true}
default_group_policy:
  concurrency: {max_interactive_requests: 1, max_queued_requests: 10, priority: normal, allow_long_requests: true}
  reply: {command_prefix: /, continuation_window: 0s, debounce: 20ms, min_reply_interval: 0s, user_rate_limit_per_minute: 30}
  context: {max_recent_messages: 50, max_context_tokens: 0}
  review: {level: basic, review_input: true, review_output: true, action: block}
runtime:
  outbox_lease: LEASE
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

// testLogger logs at debug level and prints the logs if the test fails.
func testLogger(t *testing.T, name string) (*slog.Logger, *syncBuffer) {
	logs := &syncBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("%s logs:\n%s", name, logs)
		}
	})
	return slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), logs
}

// core is a real ConLoom core behind a recording proxy that can inject failures.
type core struct {
	t       *testing.T
	dataDir string
	app     *app.App
	srv     *httptest.Server
	proxy   *httptest.Server
	URL     string

	failAcks   atomic.Bool
	failEvents atomic.Bool

	mu         sync.Mutex
	events     []conloomapi.Event
	eventCodes []int
	acks       []ackRecord

	closeOnce sync.Once
}

type ackRecord struct {
	ActionID string
	Req      conloomapi.AckRequest
	Code     int
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func newCore(t *testing.T, lease string) *core {
	t.Helper()
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return 200, fakeReply })
	dir := t.TempDir()
	c := &core{t: t, dataDir: filepath.Join(dir, "data")}
	yml := strings.NewReplacer("DATA_DIR", strconv.Quote(c.dataDir), "MODEL_URL", fake.URL, "LEASE", lease).Replace(coreConfig)
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
	logger, _ := testLogger(t, "core")
	c.app, err = app.New(cfg, logger, app.Options{})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if err := c.app.Start(context.Background()); err != nil {
		_ = c.app.Close()
		t.Fatalf("Start: %v", err)
	}
	c.srv = httptest.NewServer(c.app.Handler())
	target, _ := url.Parse(c.srv.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorLog = log.New(io.Discard, "", 0)
	c.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.intercept(w, r, rp)
	}))
	c.URL = c.proxy.URL
	t.Cleanup(c.close)
	return c
}

func (c *core) intercept(w http.ResponseWriter, r *http.Request, next http.Handler) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/events":
		var ev conloomapi.Event
		if err := json.Unmarshal(body, &ev); err != nil {
			c.t.Errorf("proxy: bad event body: %v", err)
		}
		if c.failEvents.Load() {
			http.Error(rec, "core unavailable", http.StatusServiceUnavailable)
		} else {
			next.ServeHTTP(rec, r)
		}
		c.mu.Lock()
		c.events = append(c.events, ev)
		c.eventCodes = append(c.eventCodes, rec.code)
		c.mu.Unlock()
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"):
		var req conloomapi.AckRequest
		if err := json.Unmarshal(body, &req); err != nil {
			c.t.Errorf("proxy: bad ack body: %v", err)
		}
		if c.failAcks.Load() {
			http.Error(rec, "bad gateway", http.StatusBadGateway)
		} else {
			next.ServeHTTP(rec, r)
		}
		actionID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/outbox/"), "/ack")
		c.mu.Lock()
		c.acks = append(c.acks, ackRecord{ActionID: actionID, Req: req, Code: rec.code})
		c.mu.Unlock()
	default:
		next.ServeHTTP(w, r)
	}
}

func (c *core) close() {
	c.closeOnce.Do(func() {
		c.proxy.Close()
		c.srv.Close()
		if err := c.app.Close(); err != nil {
			c.t.Errorf("app.Close: %v", err)
		}
	})
}

// eventsFor returns the submitted events with the given platform message ID
// and their response codes.
func (c *core) eventsFor(platformMsgID string) (n int, codes []int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ev := range c.events {
		if ev.Message.PlatformMessageID == platformMsgID {
			n++
			codes = append(codes, c.eventCodes[i])
		}
	}
	return n, codes
}

func (c *core) eventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *core) ackRecords() []ackRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ackRecord(nil), c.acks...)
}

// openStore closes the core and opens its database.
func (c *core) openStore() *sqlite.DB {
	c.t.Helper()
	c.close()
	db, err := sqlite.Open(context.Background(), filepath.Join(c.dataDir, "conloom.db"))
	if err != nil {
		c.t.Fatalf("sqlite.Open: %v", err)
	}
	c.t.Cleanup(func() { _ = db.Close() })
	return db
}

func napcatConfig(ob *fakeOB, c *core, stateDir string) config.NapCatConfig {
	return config.NapCatConfig{
		WSURL:         ob.URL,
		AccessToken:   fakeOBToken,
		CoreURL:       c.URL,
		CoreToken:     testCoreToken,
		BotInstanceID: testBotID,
		StateDir:      stateDir,
	}
}

type runningAdapter struct {
	a      *Adapter
	cancel context.CancelFunc
	done   chan error
	logs   *syncBuffer
	once   sync.Once
}

// stop cancels Run and waits for it to return nil.
func (r *runningAdapter) stop(t *testing.T) {
	r.once.Do(func() {
		r.cancel()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("Run returned %v, want nil after cancel", err)
			}
		case <-time.After(15 * time.Second):
			t.Errorf("Run did not return after cancel")
		}
	})
}

// startAdapter runs an adapter until the test ends. tune adjusts its timings.
func startAdapter(t *testing.T, cfg config.NapCatConfig, tune func(*Adapter)) *runningAdapter {
	t.Helper()
	logger, logs := testLogger(t, "adapter")
	a, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tune != nil {
		tune(a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningAdapter{a: a, cancel: cancel, done: make(chan error, 1), logs: logs}
	go func() { r.done <- a.Run(ctx) }()
	t.Cleanup(func() { r.stop(t) })
	return r
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// requireNoLeak fails if the logs contain message text or the access token.
func requireNoLeak(t *testing.T, logs *syncBuffer, texts ...string) {
	t.Helper()
	out := logs.String()
	for _, s := range append(texts, fakeOBToken, fakeReply) {
		if strings.Contains(out, s) {
			t.Errorf("adapter logs contain %q", s)
		}
	}
}

func sentSegments(t *testing.T, call obCall) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(call.Params["message"])
	if err != nil {
		t.Fatal(err)
	}
	var segs []map[string]any
	if err := json.Unmarshal(raw, &segs); err != nil {
		t.Fatalf("message is not a segment array: %s", raw)
	}
	return segs
}

func TestConn_CallCorrelatesEcho(t *testing.T) {
	ob := newFakeOB(t)
	ctx := context.Background()
	conn, err := Dial(ctx, ob.URL, fakeOBToken)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	var wg sync.WaitGroup
	results := make([]string, 10)
	errs := make([]error, 10)
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := conn.Call(ctx, "test_echo", map[string]any{"n": i})
			errs[i] = err
			results[i] = string(data)
		}()
	}
	wg.Wait()
	for i := range 10 {
		if errs[i] != nil {
			t.Errorf("call %d: %v", i, errs[i])
			continue
		}
		if want := `{"n":` + strconv.Itoa(i) + `}`; results[i] != want {
			t.Errorf("call %d: data = %s, want %s", i, results[i], want)
		}
	}

	if _, err := conn.Call(ctx, "no_such_action", nil); err == nil || !strings.Contains(err.Error(), "1404") {
		t.Errorf("failed action: err = %v, want retcode 1404 error", err)
	}

	select {
	case raw := <-conn.Events():
		if !strings.Contains(string(raw), `"lifecycle"`) {
			t.Errorf("first event = %s, want the lifecycle meta event", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no lifecycle event received")
	}

	if err := conn.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed after Close")
	}
	if _, err := conn.Call(ctx, "get_login_info", nil); err == nil {
		t.Error("Call after Close succeeded")
	}
}

func TestConn_RejectsBadToken(t *testing.T) {
	ob := newFakeOB(t)
	_, err := Dial(context.Background(), ob.URL, "wrong-token-value")
	if err == nil {
		t.Fatal("Dial with a wrong token succeeded")
	}
	if strings.Contains(err.Error(), "wrong-token-value") {
		t.Errorf("Dial error leaks the token: %v", err)
	}
	if ob.Accepted() != 0 {
		t.Errorf("server accepted %d connections, want 0", ob.Accepted())
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "ws://" + ln.Addr().String() + "/"
	ln.Close()
	_, err = Dial(context.Background(), closedURL, "secret+token/value")
	if err == nil {
		t.Fatal("Dial to a closed port succeeded")
	}
	if msg := err.Error(); strings.Contains(msg, "secret") || !strings.Contains(msg, "REDACTED") {
		t.Errorf("Dial error = %q, want the token redacted", msg)
	}
}

func TestAdapter_GroupMentionRoundTrip(t *testing.T) {
	ob := newFakeOB(t)
	c := newCore(t, "60s")
	r := startAdapter(t, napcatConfig(ob, c, t.TempDir()), nil)
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	ob.Push(groupMessage(10001, 20001, 70001, "在吗？", true))
	waitFor(t, 2*time.Second, "send_group_msg", func() bool { return len(ob.Calls("send_group_msg")) == 1 })

	call := ob.Calls("send_group_msg")[0]
	if gid, _ := call.Params["group_id"].(float64); gid != 10001 {
		t.Errorf("group_id = %v, want 10001", call.Params["group_id"])
	}
	segs := sentSegments(t, call)
	if len(segs) != 2 || segs[0]["type"] != "reply" || segs[1]["type"] != "text" {
		t.Fatalf("message = %v, want reply + text segments", segs)
	}
	if id := segs[0]["data"].(map[string]any)["id"]; id != "70001" {
		t.Errorf("reply id = %v, want 70001", id)
	}
	if text := segs[1]["data"].(map[string]any)["text"]; text != fakeReply {
		t.Errorf("text = %v, want %q", text, fakeReply)
	}

	waitFor(t, 2*time.Second, "the ACK", func() bool { return len(c.ackRecords()) == 1 })
	ack := c.ackRecords()[0]
	if ack.Code != http.StatusOK || ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "50001" {
		t.Errorf("ack = %+v, want 200 sent with platform message 50001", ack)
	}
	r.stop(t)
	requireNoLeak(t, r.logs, "在吗？")
}

func TestAdapter_SelfAndDisabledGroupNotForwarded(t *testing.T) {
	ob := newFakeOB(t)
	c := newCore(t, "60s")
	r := startAdapter(t, napcatConfig(ob, c, t.TempDir()), nil)
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	ob.Push(groupMessage(10001, fakeSelfID, 70001, "自己发的", true))
	sent := groupMessage(10001, fakeSelfID, 70002, "自己发的", false)
	sent["post_type"] = "message_sent"
	ob.Push(sent)
	ob.Push(groupMessage(10002, 20001, 70003, "别的群", true))
	ob.Push(map[string]any{"time": time.Now().Unix(), "self_id": fakeSelfID, "post_type": "meta_event",
		"meta_event_type": "heartbeat", "status": map[string]any{"online": true, "good": true}, "interval": 30000})
	ob.Push(groupMessage(10001, 20001, 70004, "在吗？", true))
	waitFor(t, 5*time.Second, "send_group_msg for the enabled group", func() bool { return len(ob.Calls("send_group_msg")) == 1 })

	if n := c.eventCount(); n != 1 {
		t.Errorf("core received %d events, want only the enabled group's message", n)
	}
	if n, _ := c.eventsFor("70004"); n != 1 {
		t.Errorf("enabled group message submitted %d times, want 1", n)
	}
	r.stop(t)
	requireNoLeak(t, r.logs, "自己发的", "别的群")

	db := c.openStore()
	ctx := context.Background()
	for _, id := range []string{"msg_70001", "msg_70002", "msg_70003"} {
		if _, err := db.GetMessage(ctx, testBotID, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetMessage(%s) err = %v, want not found", id, err)
		}
	}
	if _, err := db.GetMessage(ctx, testBotID, "msg_70004"); err != nil {
		t.Errorf("GetMessage(msg_70004): %v", err)
	}
}

func TestAdapter_ReconnectsAfterServerDrop(t *testing.T) {
	ob := newFakeOB(t)
	c := newCore(t, "60s")
	startAdapter(t, napcatConfig(ob, c, t.TempDir()), nil)
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	ob.Push(groupMessage(10001, 20001, 70001, "在吗？", true))
	waitFor(t, 2*time.Second, "first send_group_msg", func() bool { return len(ob.Calls("send_group_msg")) == 1 })

	ob.Drop()
	waitFor(t, 10*time.Second, "reconnect", func() bool { return len(ob.Calls("get_login_info")) == 2 })
	ob.Push(groupMessage(10001, 20002, 70002, "还在吗？", true))
	waitFor(t, 5*time.Second, "send_group_msg after reconnect", func() bool { return len(ob.Calls("send_group_msg")) == 2 })

	segs := sentSegments(t, ob.Calls("send_group_msg")[1])
	if id := segs[0]["data"].(map[string]any)["id"]; id != "70002" {
		t.Errorf("reply id after reconnect = %v, want 70002", id)
	}
	if ob.Accepted() != 2 {
		t.Errorf("connections = %d, want 2", ob.Accepted())
	}
}

func TestAdapter_AckFailureDoesNotResend(t *testing.T) {
	ob := newFakeOB(t)
	c := newCore(t, "300ms")
	c.failAcks.Store(true)
	startAdapter(t, napcatConfig(ob, c, t.TempDir()), nil)
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	ob.Push(groupMessage(10001, 20001, 70001, "在吗？", true))
	waitFor(t, 2*time.Second, "send_group_msg", func() bool { return len(ob.Calls("send_group_msg")) == 1 })
	waitFor(t, 2*time.Second, "the failed ACK", func() bool { return len(c.ackRecords()) == 1 })
	c.failAcks.Store(false)

	waitFor(t, 10*time.Second, "a successful ACK after redelivery", func() bool {
		acks := c.ackRecords()
		return acks[len(acks)-1].Code == http.StatusOK
	})
	acks := c.ackRecords()
	first, last := acks[0], acks[len(acks)-1]
	if first.Code != http.StatusBadGateway {
		t.Errorf("first ACK code = %d, want 502", first.Code)
	}
	if last.ActionID != first.ActionID || last.Req.Status != conloomapi.AckSent || last.Req.PlatformMessageID != "50001" {
		t.Errorf("final ACK = %+v, want sent for %s with platform message 50001", last, first.ActionID)
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(ob.Calls("send_group_msg")); n != 1 {
		t.Errorf("send_group_msg calls = %d, want 1", n)
	}
}

func TestAdapter_RestartWithDedupDoesNotResend(t *testing.T) {
	ob := newFakeOB(t)
	c := newCore(t, "300ms")
	c.failAcks.Store(true)
	stateDir := t.TempDir()
	r1 := startAdapter(t, napcatConfig(ob, c, stateDir), nil)
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	ob.Push(groupMessage(10001, 20001, 70001, "在吗？", true))
	waitFor(t, 2*time.Second, "send_group_msg", func() bool { return len(ob.Calls("send_group_msg")) == 1 })
	waitFor(t, 2*time.Second, "the failed ACK", func() bool { return len(c.ackRecords()) >= 1 })
	r1.stop(t)
	if _, err := os.Stat(filepath.Join(stateDir, "napcat-dedup.log")); err != nil {
		t.Fatalf("dedup log: %v", err)
	}

	c.failAcks.Store(false)
	startAdapter(t, napcatConfig(ob, c, stateDir), nil)
	waitFor(t, 10*time.Second, "a successful ACK from the new adapter", func() bool {
		acks := c.ackRecords()
		return acks[len(acks)-1].Code == http.StatusOK
	})
	acks := c.ackRecords()
	last := acks[len(acks)-1]
	if last.ActionID != acks[0].ActionID || last.Req.Status != conloomapi.AckSent || last.Req.PlatformMessageID != "50001" {
		t.Errorf("final ACK = %+v, want sent for %s with platform message 50001", last, acks[0].ActionID)
	}
	if n := len(ob.Calls("send_group_msg")); n != 1 {
		t.Errorf("send_group_msg calls = %d, want 1", n)
	}
}

func TestConn_CallReportsWhetherTheRequestWasSent(t *testing.T) {
	ob := newFakeOB(t)
	ob.SetSend(sendReplyThenDrop, 0)
	ctx := context.Background()
	for i := range 20 {
		conn, err := Dial(ctx, ob.URL, fakeOBToken)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		data, err := conn.Call(ctx, "send_group_msg", map[string]any{"group_id": 10001})
		if err != nil || !strings.Contains(string(data), "message_id") {
			t.Fatalf("attempt %d: response followed by a drop: data = %s, err = %v, want the response", i, data, err)
		}
		select {
		case <-conn.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("connection not done after the server dropped it")
		}
		if _, err := conn.Call(ctx, "send_group_msg", nil); !errors.Is(err, errNotSent) {
			t.Errorf("Call on a closed connection: err = %v, want errNotSent", err)
		}
		_ = conn.Close()
	}

	ob.SetSend(sendNoReply, 0)
	conn, err := Dial(ctx, ob.URL, fakeOBToken)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	tctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := conn.Call(tctx, "send_group_msg", map[string]any{"group_id": 10001}); err == nil || errors.Is(err, errNotSent) {
		t.Errorf("unanswered Call: err = %v, want an error other than errNotSent", err)
	}
}

func TestAdapter_ResponseThenDropDoesNotResend(t *testing.T) {
	ob := newFakeOB(t)
	ob.SetSend(sendReplyThenDrop, 0)
	c := newCore(t, "300ms")
	startAdapter(t, napcatConfig(ob, c, t.TempDir()), nil)
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	ob.Push(groupMessage(10001, 20001, 70001, "在吗？", true))
	waitFor(t, 2*time.Second, "send_group_msg", func() bool { return len(ob.Calls("send_group_msg")) == 1 })
	waitFor(t, 2*time.Second, "the ACK", func() bool { return len(c.ackRecords()) == 1 })
	waitFor(t, 10*time.Second, "reconnect", func() bool { return len(ob.Calls("get_login_info")) == 2 })
	time.Sleep(800 * time.Millisecond)

	if n := len(ob.Calls("send_group_msg")); n != 1 {
		t.Errorf("send_group_msg calls = %d, want 1", n)
	}
	acks := c.ackRecords()
	if len(acks) != 1 || acks[0].Code != http.StatusOK || acks[0].Req.Status != conloomapi.AckSent || acks[0].Req.PlatformMessageID != "50001" {
		t.Errorf("acks = %+v, want one 200 sent with platform message 50001", acks)
	}
}

func TestAdapter_SendTimeoutIsNotResent(t *testing.T) {
	ob := newFakeOB(t)
	ob.SetSend(sendNoReply, 0)
	c := newCore(t, "300ms")
	c.failAcks.Store(true)
	stateDir := t.TempDir()
	startAdapter(t, napcatConfig(ob, c, stateDir), func(a *Adapter) { a.sendTimeout = 200 * time.Millisecond })
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	ob.Push(groupMessage(10001, 20001, 70001, "在吗？", true))
	waitFor(t, 2*time.Second, "send_group_msg", func() bool { return len(ob.Calls("send_group_msg")) == 1 })
	waitFor(t, 2*time.Second, "the failed ACK", func() bool { return len(c.ackRecords()) == 1 })
	first := c.ackRecords()[0]
	if first.Req.Status != conloomapi.AckFailed {
		t.Errorf("ACK after the send timed out = %+v, want failed", first)
	}
	c.failAcks.Store(false)

	waitFor(t, 10*time.Second, "a successful ACK after redelivery", func() bool {
		acks := c.ackRecords()
		return acks[len(acks)-1].Code == http.StatusOK
	})
	last := c.ackRecords()[len(c.ackRecords())-1]
	if last.ActionID != first.ActionID || last.Req.Status != conloomapi.AckFailed || last.Req.PlatformMessageID != "" {
		t.Errorf("final ACK = %+v, want failed for %s", last, first.ActionID)
	}
	time.Sleep(800 * time.Millisecond)
	if n := len(ob.Calls("send_group_msg")); n != 1 {
		t.Errorf("send_group_msg calls = %d, want 1", n)
	}
	record, err := os.ReadFile(filepath.Join(stateDir, "napcat-dedup.log"))
	if err != nil || !strings.Contains(string(record), first.ActionID+"\t") {
		t.Errorf("dedup log = %q (err %v), want a record of %s", record, err, first.ActionID)
	}
}

func TestAdapter_ShutdownWaitsForInFlightSend(t *testing.T) {
	ob := newFakeOB(t)
	ob.SetSend(sendDelayedReply, 700*time.Millisecond)
	c := newCore(t, "300ms")
	c.failAcks.Store(true)
	stateDir := t.TempDir()
	r1 := startAdapter(t, napcatConfig(ob, c, stateDir), nil)
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	ob.Push(groupMessage(10001, 20001, 70001, "在吗？", true))
	waitFor(t, 2*time.Second, "send_group_msg", func() bool { return len(ob.Calls("send_group_msg")) == 1 })
	r1.stop(t)
	acks := c.ackRecords()
	if len(acks) != 1 || acks[0].Req.Status != conloomapi.AckSent || acks[0].Req.PlatformMessageID != "50001" {
		t.Fatalf("ACKs during shutdown = %+v, want one sent with platform message 50001", acks)
	}
	record, err := os.ReadFile(filepath.Join(stateDir, "napcat-dedup.log"))
	if err != nil || !strings.Contains(string(record), acks[0].ActionID+"\t") || !strings.Contains(string(record), "\t50001\n") {
		t.Errorf("dedup log = %q (err %v), want %s recorded with 50001", record, err, acks[0].ActionID)
	}

	ob.SetSend(sendReply, 0)
	c.failAcks.Store(false)
	startAdapter(t, napcatConfig(ob, c, stateDir), nil)
	waitFor(t, 10*time.Second, "a successful ACK from the new adapter", func() bool {
		acks := c.ackRecords()
		return acks[len(acks)-1].Code == http.StatusOK
	})
	last := c.ackRecords()[len(c.ackRecords())-1]
	if last.ActionID != acks[0].ActionID || last.Req.Status != conloomapi.AckSent || last.Req.PlatformMessageID != "50001" {
		t.Errorf("final ACK = %+v, want sent for %s with platform message 50001", last, acks[0].ActionID)
	}
	if n := len(ob.Calls("send_group_msg")); n != 1 {
		t.Errorf("send_group_msg calls = %d, want 1", n)
	}
}

func TestAdapter_BuffersEventsWhileCoreUnavailable(t *testing.T) {
	ob := newFakeOB(t)
	c := newCore(t, "60s")
	startAdapter(t, napcatConfig(ob, c, t.TempDir()), func(a *Adapter) {
		a.submitDelays = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
		a.retryInterval = 200 * time.Millisecond
	})
	waitFor(t, 5*time.Second, "get_login_info", func() bool { return len(ob.Calls("get_login_info")) == 1 })

	c.failEvents.Store(true)
	ob.Push(groupMessage(10001, 20001, 70001, "在吗？", true))
	waitFor(t, 5*time.Second, "four submit attempts", func() bool { n, _ := c.eventsFor("70001"); return n >= 4 })
	time.Sleep(50 * time.Millisecond)
	if n, _ := c.eventsFor("70001"); n != 4 {
		t.Errorf("submit attempts before buffering = %d, want 4 (1 + 3 retries)", n)
	}
	c.failEvents.Store(false)

	waitFor(t, 5*time.Second, "send_group_msg after the core recovers", func() bool { return len(ob.Calls("send_group_msg")) == 1 })
	_, codes := c.eventsFor("70001")
	if codes[len(codes)-1] != http.StatusOK && codes[len(codes)-1] != http.StatusAccepted {
		t.Errorf("final submit status = %d, want success", codes[len(codes)-1])
	}
}

func TestNew_RequiresConfig(t *testing.T) {
	if _, err := New(config.NapCatConfig{}, nil); err == nil {
		t.Error("New with an empty config succeeded")
	}
}
