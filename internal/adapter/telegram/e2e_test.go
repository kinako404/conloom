package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const realCoreConfig = `
server:
  listen: 127.0.0.1:0
storage:
  data_dir: DATA_DIR
api_tokens:
  - name: telegram
    token: fake-tg-adapter-token
    bot_instance_id: bot_tg
    scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]
bots:
  - id: bot_tg
    platform: telegram
    name: 织语
    platform_self_id: "30001"
    admins: ["20001"]
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
`

// realCore is a ConLoom core behind a proxy that records the adapter's
// submissions and ACKs.
type realCore struct {
	t       *testing.T
	dataDir string
	app     *app.App
	srv     *httptest.Server
	proxy   *httptest.Server
	model   *fakeopenai.Server
	URL     string

	mu      sync.Mutex
	submits []submitRecord
	acks    []ackRecord
	once    sync.Once
}

func newRealCore(t *testing.T) *realCore {
	t.Helper()
	c := &realCore{t: t, dataDir: filepath.Join(t.TempDir(), "data")}
	c.model = fakeopenai.New(t, func(map[string]any) (int, string) { return 200, replyText })
	dir := t.TempDir()
	yml := strings.NewReplacer("DATA_DIR", strconv.Quote(c.dataDir), "MODEL_URL", c.model.URL).Replace(realCoreConfig)
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
	if c.app, err = app.New(cfg, logger, app.Options{}); err != nil {
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
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		rp.ServeHTTP(rec, r)
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/events":
			var ev conloomapi.Event
			_ = json.Unmarshal(body, &ev)
			c.submits = append(c.submits, submitRecord{Key: r.Header.Get("Idempotency-Key"), Event: ev, Code: rec.code, At: time.Now()})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"):
			var req conloomapi.AckRequest
			_ = json.Unmarshal(body, &req)
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/outbox/"), "/ack")
			c.acks = append(c.acks, ackRecord{ActionID: id, Req: req, Code: rec.code})
		}
	}))
	c.URL = c.proxy.URL
	t.Cleanup(c.close)
	return c
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

func (c *realCore) close() {
	c.once.Do(func() {
		c.proxy.Close()
		c.srv.Close()
		if err := c.app.Close(); err != nil {
			c.t.Errorf("app.Close: %v", err)
		}
	})
}

func (c *realCore) Acks() []ackRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ackRecord(nil), c.acks...)
}

func (c *realCore) Submits() []submitRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]submitRecord(nil), c.submits...)
}

// lastUserContent is the content of the last user message of a model request.
func lastUserContent(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		if m, ok := msgs[i].(map[string]any); ok && m["role"] == "user" {
			s, _ := m["content"].(string)
			return s
		}
	}
	return ""
}

func TestE2E_PrivateChatWithRealCore(t *testing.T) {
	tg, core := newFakeTG(t), newRealCore(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("telegram calls:\n%s", tg)
		}
	})
	stateDir := t.TempDir()
	r1 := startAdapter(t, tgConfig(tg.URL, core.URL, stateDir), nil)

	// 1. 小明 says hello; the reply goes back to his chat and is ACKed with
	// the chat-scoped platform message ID.
	tg.Push(tgUpdate(101, privateMessage(11, map[string]any{"text": "在吗？", "date": time.Now().Unix()})))
	waitFor(t, 10*time.Second, "the first reply", func() bool { return len(tg.Calls("sendMessage")) == 1 })
	first := tg.Calls("sendMessage")[0]
	if first.num("chat_id") != userXM || first.text() != replyText {
		t.Errorf("sendMessage = %v", first.Params)
	}
	waitFor(t, 5*time.Second, "the first ACK", func() bool { return len(core.Acks()) == 1 })
	if ack := core.Acks()[0]; ack.Code != http.StatusOK || ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "20001_501" {
		t.Fatalf("ack = %+v, want 200 sent with platform message 20001_501", ack)
	}

	// 2. He quotes the bot's reply: the core must resolve reply_to to the
	// bot's own message, so the model sees what he is replying to.
	botMsg := map[string]any{
		"message_id": 501, "date": 1794470401, "text": replyText,
		"from": map[string]any{"id": fakeBotID, "is_bot": true, "first_name": "织语", "username": fakeBotUsername},
		"chat": map[string]any{"id": userXM, "type": "private"},
	}
	tg.Push(tgUpdate(102, privateMessage(12, map[string]any{"text": "真的吗？", "reply_to_message": botMsg, "date": time.Now().Unix()})))
	waitFor(t, 10*time.Second, "the second reply", func() bool { return len(tg.Calls("sendMessage")) == 2 })
	reqs := core.model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model requests = %d, want 2", len(reqs))
	}
	prompt := lastUserContent(reqs[1])
	if !strings.Contains(prompt, "（回复 你（织语）") || !strings.Contains(prompt, replyText) {
		t.Errorf("second prompt does not show a reply to the bot's message:\n%s", prompt)
	}
	if strings.Contains(prompt, "回复了一条更早的消息") {
		t.Errorf("the core did not find the quoted bot message:\n%s", prompt)
	}
	waitFor(t, 5*time.Second, "the second ACK", func() bool { return len(core.Acks()) == 2 })
	waitFor(t, 5*time.Second, "update 102 confirmed", func() bool { return hasOffset(tg, 103) })
	if err := r1.stop(t); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 3. Telegram hands update 102 to a restarted adapter again (as it does
	// for an update that was never confirmed): the core replays it and no
	// second reply is sent.
	tg.Push(tgUpdate(102, privateMessage(12, map[string]any{"text": "真的吗？", "reply_to_message": botMsg, "date": time.Now().Unix()})))
	r2 := startAdapter(t, tgConfig(tg.URL, core.URL, stateDir), nil)
	waitFor(t, 10*time.Second, "the redelivered update handled", func() bool {
		n := 0
		for _, s := range core.Submits() {
			if s.Key == "telegram:30001:102" {
				n++
			}
		}
		return n == 2
	})
	time.Sleep(500 * time.Millisecond)
	if n := len(tg.Calls("sendMessage")); n != 2 {
		t.Errorf("sendMessage calls = %d after the replayed update, want 2", n)
	}
	if n := len(core.model.Requests()); n != 2 {
		t.Errorf("model requests = %d after the replayed update, want 2", n)
	}
	if err := r2.stop(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireNoText(t, r1.logs, "在吗？", "真的吗？", replyText)
	requireNoText(t, r2.logs, "在吗？", "真的吗？", replyText)

	// The stored messages carry the aligned IDs.
	core.close()
	db, err := sqlite.Open(context.Background(), filepath.Join(core.dataDir, "conloom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bot, err := db.GetMessage(context.Background(), testBotID, "msg_20001_501")
	if err != nil || !bot.FromBot || bot.PlatformMessageID != "20001_501" {
		t.Errorf("bot message = %+v (err %v), want msg_20001_501 from the bot", bot, err)
	}
	quote, err := db.GetMessage(context.Background(), testBotID, "msg_20001_12")
	if err != nil || quote.ReplyTo != "msg_20001_501" || quote.FromBot {
		t.Errorf("user message = %+v (err %v), want msg_20001_12 replying to msg_20001_501", quote, err)
	}
}
