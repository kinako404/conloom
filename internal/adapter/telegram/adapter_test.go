package telegram

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const replyText = "你好呀，我在。"

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

func tgConfig(apiBase, coreURL, stateDir string) config.TelegramConfig {
	return config.TelegramConfig{
		Token: fakeToken, APIBase: apiBase, PollTimeout: conloomapi.Duration{Duration: time.Second},
		CoreURL: coreURL, CoreToken: testCoreToken, BotInstanceID: testBotID, StateDir: stateDir,
	}
}

type runningAdapter struct {
	a      *Adapter
	cancel context.CancelFunc
	done   chan error
	logs   *syncBuffer
	once   sync.Once
	err    error
}

// stop cancels Run and waits for it to return; it fails the test if Run
// does not return within 15s.
func (r *runningAdapter) stop(t *testing.T) error {
	r.once.Do(func() {
		r.cancel()
		select {
		case r.err = <-r.done:
		case <-time.After(15 * time.Second):
			t.Errorf("Run did not return after cancel")
		}
	})
	return r.err
}

// startAdapter runs an adapter with fast test timings until the test ends.
// Every test also checks that the adapter never logged the bot token.
func startAdapter(t *testing.T, cfg config.TelegramConfig, tune func(*Adapter)) *runningAdapter {
	t.Helper()
	logger, logs := testLogger(t, "adapter")
	a, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.outboxWait = 200 * time.Millisecond
	a.outboxErrorDelay = 20 * time.Millisecond
	a.retryMin, a.retryMax = 20*time.Millisecond, 200*time.Millisecond
	a.submitMin, a.submitMax = 20*time.Millisecond, 200*time.Millisecond
	if tune != nil {
		tune(a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningAdapter{a: a, cancel: cancel, done: make(chan error, 1), logs: logs}
	go func() { r.done <- a.Run(ctx) }()
	t.Cleanup(func() {
		if err := r.stop(t); err != nil {
			t.Errorf("Run returned %v, want nil after cancel", err)
		}
		requireNoToken(t, "adapter logs", logs.String(), fakeToken)
	})
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

func requireNoText(t *testing.T, logs *syncBuffer, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if strings.Contains(logs.String(), s) {
			t.Errorf("adapter logs contain message text %q", s)
		}
	}
}

func hasOffset(tg *fakeTG, offset int64) bool {
	for _, o := range tg.Offsets() {
		if o == offset {
			return true
		}
	}
	return false
}

func TestAdapter_PrivateRoundTrip(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.set(func(c *fakeCore) {
		c.onEvent = func(ev conloomapi.Event) { core.Enqueue(replyAction("act_1", replyText, "")) }
	})
	r := startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)

	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "sendMessage", func() bool { return len(tg.Calls("sendMessage")) == 1 })
	waitFor(t, 2*time.Second, "the ACK", func() bool { return len(core.Acks()) == 1 })

	if keys := core.Accepted(); len(keys) != 1 || keys[0] != "telegram:30001:101" {
		t.Errorf("accepted keys = %v, want [telegram:30001:101]", keys)
	}
	ev := core.Submits()[0].Event
	if ev.Message.ID != "msg_20001_11" || ev.Conversation.ID != "private_20001" || ev.Message.Text != "在吗？" {
		t.Errorf("event = %+v", ev)
	}
	call := tg.Calls("sendMessage")[0]
	if call.num("chat_id") != userXM || call.text() != replyText {
		t.Errorf("sendMessage = %v", call.Params)
	}
	for _, k := range []string{"parse_mode", "reply_parameters"} {
		if _, ok := call.Params[k]; ok {
			t.Errorf("sendMessage sets %s: %v", k, call.Params)
		}
	}
	ack := core.Acks()[0]
	if ack.ActionID != "act_1" || ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "20001_501" {
		t.Errorf("ack = %+v, want sent with platform message 20001_501", ack)
	}
	waitFor(t, 5*time.Second, "the update confirmed", func() bool { return hasOffset(tg, 102) })
	r.stop(t)
	requireNoText(t, r.logs, "在吗？", replyText)
}

func TestAdapter_OffsetAdvancesOnlyAfterCoreAccepts(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.set(func(c *fakeCore) { c.failEvents = 2 })
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
		a.submitMin, a.submitMax = 100*time.Millisecond, time.Second
	})

	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "the core to accept the update", func() bool { return len(core.Accepted()) == 1 })
	waitFor(t, 5*time.Second, "offset 102", func() bool { return hasOffset(tg, 102) })

	subs := core.Submits()
	if len(subs) != 3 {
		t.Fatalf("submissions = %d, want 2 failures and 1 success", len(subs))
	}
	for _, s := range subs {
		if s.Key != "telegram:30001:101" {
			t.Errorf("retry used key %q, want the same key every time", s.Key)
		}
	}
	if g1, g2 := subs[1].At.Sub(subs[0].At), subs[2].At.Sub(subs[1].At); g1 < 100*time.Millisecond || g2 < 200*time.Millisecond {
		t.Errorf("retry gaps = %v, %v; want a backoff of at least 100ms then 200ms", g1, g2)
	}
	accepted := subs[2].At
	for _, c := range tg.Calls("getUpdates") {
		if c.num("offset") > 101 && c.At.Before(accepted) {
			t.Errorf("getUpdates confirmed update 101 (offset %d) before the core accepted it", c.num("offset"))
		}
	}
}

func TestAdapter_RejectedUpdateIsSkipped(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.set(func(c *fakeCore) { c.rejectNext = 1 })
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)

	tg.Push(textUpdate(101, 11, "第一条"), textUpdate(102, 12, "第二条"))
	waitFor(t, 5*time.Second, "offset 103", func() bool { return hasOffset(tg, 103) })
	if keys := core.Accepted(); len(keys) != 1 || keys[0] != "telegram:30001:102" {
		t.Errorf("accepted = %v, want only update 102 after the core rejected 101", keys)
	}
	if n := len(core.Submits()); n != 2 {
		t.Errorf("submissions = %d, want 2 (a rejected update is not retried)", n)
	}
}

func TestAdapter_SkipsChannelUpdatesAndForwardsGroups(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	r := startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)

	tg.Push(channelUpdate(101, 11, "频道公告"), groupUpdate(102, 12, "群里的话"), textUpdate(103, 13, "私聊"))
	waitFor(t, 5*time.Second, "offset 104", func() bool { return hasOffset(tg, 104) })
	if keys := core.Accepted(); len(keys) != 2 || keys[0] != "telegram:30001:102" || keys[1] != "telegram:30001:103" {
		t.Errorf("accepted = %v, want the group and the private update", keys)
	}
	if conv := core.Submits()[0].Event.Conversation; conv.ID != "group_-1001234567890" || conv.PlatformConversationID != "-1001234567890" {
		t.Errorf("group conversation = %+v", conv)
	}
	if logs := r.logs.String(); !strings.Contains(logs, "update_id=101") || !strings.Contains(logs, "level=DEBUG") {
		t.Errorf("skipped channel update not logged at debug level")
	}
	requireNoText(t, r.logs, "频道公告", "群里的话")
}

func TestAdapter_SplitsLongReplyAndQuotesOnlyFirst(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	long := strings.Repeat("好", 3000) + "\n\n" + strings.Repeat("长", 3000)
	core.Enqueue(replyAction("act_long", long, "20001_11"))
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)

	waitFor(t, 5*time.Second, "the ACK", func() bool { return len(core.Acks()) == 1 })
	calls := tg.Calls("sendMessage")
	if len(calls) != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", len(calls))
	}
	if calls[0].text() != strings.Repeat("好", 3000) || calls[1].text() != strings.Repeat("长", 3000) {
		t.Errorf("parts = %d and %d units, want the two paragraphs", len(utf16.Encode([]rune(calls[0].text()))), len(utf16.Encode([]rune(calls[1].text()))))
	}
	rp, ok := calls[0].Params["reply_parameters"].(map[string]any)
	if !ok || toInt(rp["message_id"]) != 11 || rp["allow_sending_without_reply"] != true {
		t.Errorf("first part reply_parameters = %v, want message 11 allowing sending without reply", calls[0].Params["reply_parameters"])
	}
	if _, ok := calls[1].Params["reply_parameters"]; ok {
		t.Errorf("second part quotes too: %v", calls[1].Params)
	}
	ack := core.Acks()[0]
	if ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "20001_501" {
		t.Errorf("ack = %+v, want sent with the first part's ID 20001_501", ack)
	}
}

func TestAdapter_RateLimitWaitsRetryAfter(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	tg.SetSend(func(call tgCall, n int) (int, string) {
		if n == 1 {
			return http.StatusTooManyRequests, tgError(429, "Too Many Requests: retry after 1", 1)
		}
		return 0, ""
	})
	core.Enqueue(replyAction("act_1", replyText, ""))
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)

	waitFor(t, 5*time.Second, "the ACK", func() bool { return len(core.Acks()) == 1 })
	calls := tg.Calls("sendMessage")
	if len(calls) != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", len(calls))
	}
	if gap := calls[1].At.Sub(calls[0].At); gap < time.Second {
		t.Errorf("retried after %v, want at least retry_after (1s)", gap)
	}
	if ack := core.Acks()[0]; ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "20001_501" {
		t.Errorf("ack = %+v, want sent", ack)
	}
}

func TestAdapter_UncertainOrRejectedSendIsNotResent(t *testing.T) {
	cases := []struct {
		name    string
		respond tgResponder
	}{
		{"bad gateway", func(tgCall, int) (int, string) { return http.StatusBadGateway, "<html>502 Bad Gateway</html>" }},
		{"no response", func(tgCall, int) (int, string) { return -1, "" }},
		{"server error", func(tgCall, int) (int, string) {
			return http.StatusInternalServerError, tgError(500, "Internal Server Error", 0)
		}},
		{"blocked", func(tgCall, int) (int, string) {
			return http.StatusForbidden, tgError(403, "Forbidden: bot was blocked by the user", 0)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg, core := newFakeTG(t), newFakeCore(t)
			tg.SetSend(tc.respond)
			core.set(func(c *fakeCore) { c.failAcks = true })
			core.Enqueue(replyAction("act_1", replyText, ""))
			stateDir := t.TempDir()
			startAdapter(t, tgConfig(tg.URL, core.URL, stateDir), func(a *Adapter) { a.sendTimeout = 200 * time.Millisecond })

			waitFor(t, 5*time.Second, "the failed ACK", func() bool { return len(core.Acks()) == 1 })
			if ack := core.Acks()[0]; ack.Req.Status != conloomapi.AckFailed || ack.Req.PlatformMessageID != "" {
				t.Errorf("ack = %+v, want failed", ack)
			}
			record, err := os.ReadFile(filepath.Join(stateDir, "telegram-dedup.log"))
			if err != nil || !strings.Contains(string(record), "act_1\t") {
				t.Errorf("dedup log = %q (err %v), want act_1 recorded", record, err)
			}
			core.set(func(c *fakeCore) { c.failAcks = false })
			waitFor(t, 5*time.Second, "an ACK after redelivery", func() bool {
				acks := core.Acks()
				return acks[len(acks)-1].Code == http.StatusOK
			})
			if last := core.Acks()[len(core.Acks())-1]; last.Req.Status != conloomapi.AckFailed {
				t.Errorf("ACK after redelivery = %+v, want failed", last)
			}
			time.Sleep(300 * time.Millisecond)
			if n := len(tg.Calls("sendMessage")); n != 1 {
				t.Errorf("sendMessage calls = %d, want 1", n)
			}
		})
	}
}

func TestAdapter_RestartWithDedupDoesNotResend(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.set(func(c *fakeCore) { c.failAcks = true })
	core.Enqueue(replyAction("act_1", replyText, ""))
	stateDir := t.TempDir()
	r1 := startAdapter(t, tgConfig(tg.URL, core.URL, stateDir), nil)
	waitFor(t, 5*time.Second, "the failed ACK", func() bool { return len(core.Acks()) >= 1 })
	if err := r1.stop(t); err != nil {
		t.Fatalf("Run: %v", err)
	}

	core.set(func(c *fakeCore) { c.failAcks = false })
	startAdapter(t, tgConfig(tg.URL, core.URL, stateDir), nil)
	waitFor(t, 5*time.Second, "a successful ACK from the new adapter", func() bool {
		acks := core.Acks()
		return acks[len(acks)-1].Code == http.StatusOK
	})
	last := core.Acks()[len(core.Acks())-1]
	if last.ActionID != "act_1" || last.Req.Status != conloomapi.AckSent || last.Req.PlatformMessageID != "20001_501" {
		t.Errorf("final ACK = %+v, want sent with 20001_501", last)
	}
	if n := len(tg.Calls("sendMessage")); n != 1 {
		t.Errorf("sendMessage calls = %d, want 1", n)
	}
}

func typingCalls(tg *fakeTG) []tgCall {
	var out []tgCall
	for _, c := range tg.Calls("sendChatAction") {
		if c.num("chat_id") == userXM && c.Params["action"] == "typing" {
			out = append(out, c)
		}
	}
	return out
}

func TestAdapter_TypingUntilReplySent(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
		a.typingInterval, a.typingFor = 50*time.Millisecond, 10*time.Second
	})

	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "repeated typing", func() bool { return len(typingCalls(tg)) >= 3 })
	if first := typingCalls(tg)[0]; first.At.Before(core.Submits()[0].At) {
		t.Error("typing started before the core answered")
	}
	core.Enqueue(replyAction("act_1", replyText, ""))
	waitFor(t, 5*time.Second, "the ACK", func() bool { return len(core.Acks()) == 1 })
	n := len(typingCalls(tg))
	time.Sleep(300 * time.Millisecond)
	if after := len(typingCalls(tg)); after != n {
		t.Errorf("typing calls went from %d to %d after the reply was sent, want it stopped", n, after)
	}
}

func TestAdapter_TypingStopsAfterLimit(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
		a.typingInterval, a.typingFor = 50*time.Millisecond, 200*time.Millisecond
	})
	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "typing", func() bool { return len(typingCalls(tg)) >= 1 })
	time.Sleep(600 * time.Millisecond)
	n := len(typingCalls(tg))
	if n > 6 {
		t.Errorf("typing calls = %d, want typing to stop after 200ms", n)
	}
	time.Sleep(300 * time.Millisecond)
	if after := len(typingCalls(tg)); after != n {
		t.Errorf("typing calls went from %d to %d, want no typing after the limit", n, after)
	}
}

func TestAdapter_TypingStopsWhenTaskFinishes(t *testing.T) {
	for _, status := range []string{conloomapi.TaskSucceeded, conloomapi.TaskFailed, conloomapi.TaskCancelled} {
		t.Run(status, func(t *testing.T) {
			tg, core := newFakeTG(t), newFakeCore(t)
			startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
				a.typingInterval, a.typingFor = 50*time.Millisecond, 10*time.Second
			})
			tg.Push(textUpdate(101, 11, "在吗？"))
			waitFor(t, 5*time.Second, "repeated typing", func() bool { return len(typingCalls(tg)) >= 3 })

			core.set(func(c *fakeCore) { c.taskStatus["task_1"] = status })
			time.Sleep(200 * time.Millisecond)
			n := len(typingCalls(tg))
			time.Sleep(300 * time.Millisecond)
			if after := len(typingCalls(tg)); after != n {
				t.Errorf("typing calls went from %d to %d after the task %s without a reply, want it stopped", n, after, status)
			}
		})
	}
}

func TestAdapter_TypingWithoutTasksReadFallsBackToLimit(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.set(func(c *fakeCore) {
		c.denyTasks = true
		c.taskStatus["task_1"] = conloomapi.TaskSucceeded
	})
	r := startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
		a.typingInterval, a.typingFor = 50*time.Millisecond, 600*time.Millisecond
	})
	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "typing", func() bool { return len(typingCalls(tg)) >= 1 })
	time.Sleep(300 * time.Millisecond)
	if n := len(typingCalls(tg)); n < 4 {
		t.Errorf("typing calls = %d after 300ms, want typing to go on without task reads", n)
	}
	time.Sleep(600 * time.Millisecond)
	n := len(typingCalls(tg))
	time.Sleep(300 * time.Millisecond)
	if after := len(typingCalls(tg)); after != n {
		t.Errorf("typing calls went from %d to %d, want it stopped by the time limit", n, after)
	}
	core.mu.Lock()
	reads := core.taskReads
	core.mu.Unlock()
	if reads != 1 {
		t.Errorf("task reads = %d, want 1 (no more reads once the scope is missing)", reads)
	}
	if logs := r.logs.String(); strings.Count(logs, "tasks:read") != 1 || !strings.Contains(logs, "level=WARN") {
		t.Errorf("want one warning about the missing tasks:read scope, logs:\n%s", logs)
	}
}

func TestAdapter_NoTypingWithoutReply(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.set(func(c *fakeCore) { c.disposition = conloomapi.DispositionIgnore })
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) { a.typingInterval = 50 * time.Millisecond })
	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "offset 102", func() bool { return hasOffset(tg, 102) })
	time.Sleep(200 * time.Millisecond)
	if n := len(typingCalls(tg)); n != 0 {
		t.Errorf("typing calls = %d for an ignored message, want 0", n)
	}
}

func TestAdapter_LogsNeverContainToken(t *testing.T) {
	core := newFakeCore(t)
	r := startAdapter(t, tgConfig(closedURL(t), core.URL, t.TempDir()), nil)
	waitFor(t, 5*time.Second, "repeated getMe failures", func() bool { return strings.Count(r.logs.String(), "getMe") >= 3 })
	r.stop(t)
	if !strings.Contains(r.logs.String(), "REDACTED") {
		t.Errorf("logs do not show the redacted request URL:\n%s", r.logs)
	}

	tg := newFakeTG(t)
	tg.FailUpdates(2)
	tg.SetSend(func(call tgCall, n int) (int, string) {
		if n == 1 {
			return http.StatusTooManyRequests, tgError(429, "Too Many Requests: retry after 1", 1)
		}
		return http.StatusBadGateway, "<html>502</html>"
	})
	core.Enqueue(replyAction("act_1", replyText, ""))
	r2 := startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)
	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "the ACK", func() bool { return len(core.Acks()) == 1 })
	waitFor(t, 5*time.Second, "offset 102", func() bool { return hasOffset(tg, 102) })
	r2.stop(t)
	requireNoText(t, r2.logs, "在吗？", replyText)
}

func TestAdapter_DeletesWebhookAtStart(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	tg.SetWebhook()
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)
	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "the update accepted", func() bool { return len(core.Accepted()) == 1 })

	calls := tg.Calls("deleteWebhook")
	if len(calls) != 1 {
		t.Fatalf("deleteWebhook calls = %d, want 1", len(calls))
	}
	if v, ok := calls[0].Params["drop_pending_updates"]; !ok || v != false {
		t.Errorf("deleteWebhook params = %v, want drop_pending_updates=false", calls[0].Params)
	}
	if first := tg.Calls("getUpdates")[0]; first.At.Before(calls[0].At) {
		t.Error("getUpdates called before deleteWebhook")
	}
}

func TestAdapter_ConflictLogsErrorAndBacksOff(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	tg.Conflict(2)
	r := startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
		a.retryMin, a.retryMax = 100*time.Millisecond, time.Second
	})
	tg.Push(textUpdate(101, 11, "在吗？"))
	waitFor(t, 5*time.Second, "the update accepted after the conflicts", func() bool { return len(core.Accepted()) == 1 })

	calls := tg.Calls("getUpdates")
	if g1, g2 := calls[1].At.Sub(calls[0].At), calls[2].At.Sub(calls[1].At); g1 < 100*time.Millisecond || g2 < 200*time.Millisecond {
		t.Errorf("getUpdates gaps after 409 = %v, %v; want a backoff of at least 100ms then 200ms", g1, g2)
	}
	var conflictErrors int
	for _, line := range strings.Split(r.logs.String(), "\n") {
		if strings.Contains(line, "level=ERROR") && strings.Contains(line, "another process") && strings.Contains(line, "409") {
			conflictErrors++
		}
	}
	if conflictErrors != 2 {
		t.Errorf("error lines about another process = %d, want 2; logs:\n%s", conflictErrors, r.logs)
	}
}

func TestAdapter_InvalidTokenStops(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	cfg := tgConfig(tg.URL, core.URL, t.TempDir())
	cfg.Token = "30001:wrong-token"
	logger, _ := testLogger(t, "adapter")
	a, err := New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = a.Run(ctx)
	if err == nil || ctx.Err() != nil {
		t.Fatalf("Run = %v, want an error before the deadline", err)
	}
	requireNoToken(t, "error", err.Error(), cfg.Token)
}

func TestAdapter_CoreTokenForAnotherBotStops(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.set(func(c *fakeCore) { c.botID = "bot_main" })
	logger, _ := testLogger(t, "adapter")
	a, err := New(tgConfig(tg.URL, core.URL, t.TempDir()), logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = a.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "bot_main") || !strings.Contains(err.Error(), testBotID) {
		t.Fatalf("Run = %v, want an error naming both bot instances", err)
	}
	if n := len(tg.Calls("getUpdates")); n != 0 {
		t.Errorf("getUpdates called %d times, want none before the core is checked", n)
	}
}

func TestNew_RequiresConfig(t *testing.T) {
	if _, err := New(config.TelegramConfig{}, nil); err == nil {
		t.Error("New with an empty config succeeded")
	}
}
