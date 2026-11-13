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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/client"
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
    enabled_conversations:
      - {type: group, platform_id: "-1001234567890"}EXTRA_CONVERSATIONS
    private_chat: PRIVATE_CHAT
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

// newRealCore starts a core whose bot_tg lets only its admin 小明 chat
// privately.
func newRealCore(t *testing.T) *realCore {
	t.Helper()
	return newRealCoreWith(t, "admins")
}

// newRealCoreWith starts a core with private_chat set to privateChat and the
// given users listed as private conversations.
func newRealCoreWith(t *testing.T, privateChat string, privateUsers ...string) *realCore {
	t.Helper()
	c := &realCore{t: t, dataDir: filepath.Join(t.TempDir(), "data")}
	c.model = fakeopenai.New(t, func(map[string]any) (int, string) { return 200, replyText })
	dir := t.TempDir()
	var extra strings.Builder
	for _, id := range privateUsers {
		extra.WriteString("\n      - {type: private, platform_id: " + strconv.Quote(id) + "}")
	}
	yml := strings.NewReplacer("DATA_DIR", strconv.Quote(c.dataDir), "MODEL_URL", c.model.URL,
		"EXTRA_CONVERSATIONS", extra.String(), "PRIVATE_CHAT", privateChat).Replace(realCoreConfig)
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
			var resp conloomapi.EventAccepted
			_ = json.Unmarshal(rec.body.Bytes(), &resp)
			c.submits = append(c.submits, submitRecord{
				Key: r.Header.Get("Idempotency-Key"), Event: ev, Code: rec.code, At: time.Now(),
				Resp: resp, Replayed: rec.Header().Get("Idempotent-Replayed") == "true",
			})
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

// statusRecorder passes a response through and keeps its status and body.
type statusRecorder struct {
	http.ResponseWriter
	code int
	body bytes.Buffer
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	r.body.Write(p)
	return r.ResponseWriter.Write(p)
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
	quoteUpdate := tgUpdate(102, privateMessage(12, map[string]any{"text": "真的吗？", "reply_to_message": botMsg, "date": time.Now().Unix()}))
	tg.Push(quoteUpdate)
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
	// for an update that was never confirmed): the same update, byte for byte.
	// The core replays its first response and no second reply is sent.
	tg.Push(quoteUpdate)
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
	var replays []submitRecord
	for _, s := range core.Submits() {
		if s.Key == "telegram:30001:102" {
			replays = append(replays, s)
		}
	}
	if again := replays[1]; again.Code != http.StatusAccepted || !again.Replayed || again.Resp != replays[0].Resp {
		t.Errorf("second submission of update 102: HTTP %d, replayed %v, response %+v; want 202, Idempotent-Replayed and the first response %+v",
			again.Code, again.Replayed, again.Resp, replays[0].Resp)
	}
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

func TestE2E_GroupChatWithRealCore(t *testing.T) {
	tg, core := newFakeTG(t), newRealCore(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("telegram calls:\n%s", tg)
		}
	})
	r := startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)
	now := func() int64 { return time.Now().Unix() }
	xiaohong := tgUser(20002, "小红", "", "xiaohong")
	sends := func() int { return len(tg.Calls("sendMessage")) }

	// 1. 小明 mentions the bot in the group; the reply goes back to the
	// negative chat ID and is ACKed with the chat-scoped ID.
	tg.Push(tgUpdate(201, groupMessage(11, map[string]any{
		"text": "@zhiyu_test_bot 在吗？", "entities": []any{tgEntity("mention", 0, 15)}, "date": now(),
	})))
	waitFor(t, 10*time.Second, "the first reply", func() bool { return sends() == 1 })
	if call := tg.Calls("sendMessage")[0]; call.num("chat_id") != testGroupID || call.text() != replyText {
		t.Errorf("sendMessage = %v, want the reply in chat %d", call.Params, int64(testGroupID))
	}
	waitFor(t, 5*time.Second, "the first ACK", func() bool { return len(core.Acks()) == 1 })
	if ack := core.Acks()[0]; ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "-1001234567890_501" {
		t.Fatalf("ack = %+v, want sent with -1001234567890_501", ack)
	}

	// 2. He replies to the bot's message without mentioning it: the core
	// must see a reply to the bot and answer.
	botMsg := map[string]any{
		"message_id": 501, "date": now(), "text": replyText,
		"from": map[string]any{"id": fakeBotID, "is_bot": true, "first_name": "织语", "username": fakeBotUsername},
		"chat": map[string]any{"id": testGroupID, "type": "supergroup"},
	}
	tg.Push(tgUpdate(202, groupMessage(12, map[string]any{"text": "真的吗？", "reply_to_message": botMsg, "date": now()})))
	waitFor(t, 10*time.Second, "the reply to a reply", func() bool { return sends() == 2 })
	reqs := core.model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model requests = %d, want 2", len(reqs))
	}
	if prompt := lastUserContent(reqs[1]); !strings.Contains(prompt, "（回复 你（织语）") || strings.Contains(prompt, "回复了一条更早的消息") {
		t.Errorf("second prompt does not show a reply to the bot's message:\n%s", prompt)
	}
	waitFor(t, 5*time.Second, "the second ACK", func() bool { return len(core.Acks()) == 2 })

	// 3. 小红 mentions 小明, and the admin 小明 sends a command addressed to
	// another bot: both are only observed, so nothing is sent and the group
	// is not paused.
	msg := groupMessage(13, map[string]any{"text": "@xiaoming 你看", "entities": []any{tgEntity("mention", 0, 9)}, "date": now()})
	msg["from"] = xiaohong
	tg.Push(tgUpdate(203, msg), tgUpdate(204, groupMessage(14, map[string]any{"text": "/pause@other_bot", "date": now()})))
	waitFor(t, 5*time.Second, "updates 203 and 204 handled", func() bool { return hasOffset(tg, 205) })
	time.Sleep(300 * time.Millisecond)
	if n := sends(); n != 2 {
		t.Fatalf("sendMessage calls = %d after observed messages, want 2", n)
	}
	tg.Push(tgUpdate(205, groupMessage(15, map[string]any{
		"text": "@zhiyu_test_bot 还在吗", "entities": []any{tgEntity("mention", 0, 15)}, "date": now(),
	})))
	waitFor(t, 10*time.Second, "a reply after /pause@other_bot", func() bool { return sends() == 3 })

	// 4. The negative ID works through the API path and the store.
	ps, err := client.New(core.URL, testCoreToken, nil).GetPersona(context.Background(), "group_-1001234567890")
	if err != nil || ps.ConversationID != "group_-1001234567890" {
		t.Errorf("GetPersona(group_-1001234567890) = %+v, %v", ps, err)
	}
	waitFor(t, 5*time.Second, "the third ACK", func() bool { return len(core.Acks()) == 3 })
	if err := r.stop(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireNoText(t, r.logs, "在吗？", "真的吗？", "你看", replyText)

	core.close()
	db, err := sqlite.Open(context.Background(), filepath.Join(core.dataDir, "conloom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conv, err := db.GetConversation(ctx, testBotID, "group_-1001234567890")
	if err != nil || conv.PlatformConversationID != "-1001234567890" || conv.Type != conloomapi.ConvGroup {
		t.Errorf("conversation = %+v (err %v)", conv, err)
	}
	if bot, err := db.GetMessage(ctx, testBotID, "msg_-1001234567890_501"); err != nil || !bot.FromBot {
		t.Errorf("bot message = %+v (err %v), want msg_-1001234567890_501 from the bot", bot, err)
	}
	if m, err := db.GetMessage(ctx, testBotID, "msg_-1001234567890_12"); err != nil || m.ReplyTo != "msg_-1001234567890_501" {
		t.Errorf("reply = %+v (err %v), want reply_to msg_-1001234567890_501", m, err)
	}
	m, err := db.GetMessage(ctx, testBotID, "msg_-1001234567890_13")
	want := []conloomapi.Mention{{Type: "user", ID: "username_xiaoming", Name: "xiaoming"}}
	if err != nil || m.DisplayName != "小红" || !reflect.DeepEqual(m.Mentions, want) {
		t.Errorf("mention message = %+v (err %v), want 小红 mentioning xiaoming", m, err)
	}
}

// privateFrom is a current text message from user in their private chat.
func privateFrom(messageID int64, user map[string]any, text string) map[string]any {
	msg := privateMessage(messageID, map[string]any{"text": text, "date": time.Now().Unix()})
	msg["from"] = user
	msg["chat"] = map[string]any{"id": user["id"], "type": "private", "first_name": user["first_name"]}
	return msg
}

func TestE2E_PrivateAllowlistWithRealCore(t *testing.T) {
	tg, core := newFakeTG(t), newRealCoreWith(t, "allowlist", "20002")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("telegram calls:\n%s", tg)
		}
	})
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) { a.typingInterval = 50 * time.Millisecond })
	xiaoming := tgUser(userXM, "小明", "", "xiaoming") // admin
	xiaohong := tgUser(20002, "小红", "", "xiaohong")  // listed
	xiaogang := tgUser(20003, "小刚", "", "xiaogang")  // neither

	tg.Push(tgUpdate(301, privateFrom(31, xiaogang, "喂")))
	waitFor(t, 5*time.Second, "update 301 handled", func() bool { return hasOffset(tg, 302) })
	stranger := core.Submits()[0]
	if stranger.Code != http.StatusAccepted || stranger.Resp.Disposition != conloomapi.DispositionIgnore ||
		stranger.Resp.SuppressedReason != conloomapi.SuppressedPrivateNotAllowed {
		t.Errorf("unlisted user: HTTP %d, response %+v; want 202 ignore with private_not_allowed", stranger.Code, stranger.Resp)
	}

	tg.Push(tgUpdate(302, privateFrom(32, xiaohong, "你好")), tgUpdate(303, privateFrom(33, xiaoming, "在吗？")))
	waitFor(t, 10*time.Second, "replies to the listed user and the admin", func() bool { return len(tg.Calls("sendMessage")) == 2 })
	time.Sleep(300 * time.Millisecond)
	chats := map[int64]int{}
	for _, c := range tg.Calls("sendMessage") {
		chats[c.num("chat_id")]++
	}
	if len(tg.Calls("sendMessage")) != 2 || chats[20002] != 1 || chats[userXM] != 1 {
		t.Errorf("replies per chat = %v, want one each to 20002 and 20001", chats)
	}
	for _, c := range tg.Calls("sendChatAction") {
		if c.num("chat_id") == 20003 {
			t.Error("typing shown to the unlisted user")
			break
		}
	}
	if n := len(core.model.Requests()); n != 2 {
		t.Errorf("model requests = %d, want 2 (none for the unlisted user)", n)
	}
}
