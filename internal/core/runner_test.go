package core

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestRunner_UsesHistoryAndPersona(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.submit(t, env.groupEvent("20002", "今天天气不错"))
	resp := env.submit(t, env.groupEvent("20001", "你觉得呢", mentionBot))
	if task := env.waitTask(t, resp.TaskID); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	msgs := requestMessages(t, env.short.Requests()[0])
	if sys, _ := msgs[0]["content"].(string); msgs[0]["role"] != "system" || !strings.Contains(sys, "你是织语") {
		t.Fatalf("first message = %v", msgs[0])
	}
	var all strings.Builder
	for _, m := range msgs {
		c, _ := m["content"].(string)
		all.WriteString(c + "\n")
	}
	if !strings.Contains(all.String(), "今天天气不错") {
		t.Errorf("observed message missing from request: %q", all.String())
	}
	if n := strings.Count(all.String(), "你觉得呢"); n != 1 {
		t.Errorf("trigger text appears %d times, want 1", n)
	}
	if last, _ := msgs[len(msgs)-1]["content"].(string); !strings.Contains(last, "你觉得呢") {
		t.Errorf("last message = %q", last)
	}
}

func TestRunner_ContextTooLargeSendsCopy(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		long := c.ModelProfiles["long_context"]
		long.Context = c.ModelProfiles["short_context"].Context
		c.ModelProfiles["long_context"] = long
	}})
	ev := env.groupEvent("20001", strings.Repeat("长", 3900), mentionBot)
	task := env.waitTask(t, env.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskFailed || task.Error == nil || task.Error.Code != conloomapi.CodeContextTooLarge {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != CopyTooLong {
		t.Fatalf("actions = %+v", task.Actions)
	}
	if task.Actions[0].ReplyToMessageID != "" {
		t.Errorf("copy reply target = %q, want none in a quiet group", task.Actions[0].ReplyToMessageID)
	}
	if n := env.short.Calls() + env.long.Calls(); n != 0 {
		t.Fatalf("model calls = %d, want 0", n)
	}
}

func TestRunner_ShortFailsFallsBackToLong(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{
		short: func(map[string]any) (int, string) { return 500, "boom" },
		long:  answer("长模型回复"),
	})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != "长模型回复" {
		t.Fatalf("task = %+v", task)
	}
	if task.Meta.SelectedModelRole != "long_context" || task.Meta.SelectedModel != "long-model" {
		t.Errorf("meta = %+v", task.Meta)
	}
	if s, l := env.short.Calls(), env.long.Calls(); s != 1 || l != 1 {
		t.Errorf("calls short=%d long=%d, want 1 and 1", s, l)
	}
}

func TestRunner_EmptyModelOutputSendsUnavailableCopy(t *testing.T) {
	think := answer("<think>想一想</think>")
	env := newTestOrchestrator(t, testOpts{short: think, long: think})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskFailed || task.Error == nil || task.Error.Code != conloomapi.CodeModelUnavailable {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != CopyUnavailable {
		t.Fatalf("actions = %+v", task.Actions)
	}
	if s, l := env.short.Calls(), env.long.Calls(); s != 1 || l != 1 {
		t.Errorf("calls short=%d long=%d, want 1 and 1", s, l)
	}
}

func TestRunner_OutputSanitized(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: answer("大家好[CQ:at,qq=all]")})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	if text := task.Actions[0].Content.Text; strings.Contains(text, "[CQ:") || text != "大家好" {
		t.Fatalf("action text = %q", text)
	}
	if task.Meta.ReviewResult != "sanitize" {
		t.Errorf("review result = %q", task.Meta.ReviewResult)
	}
}

func TestRunner_BlockedOutputHasNoAction(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{
		short: answer("这里有禁词"),
		configure: func(c *config.Config) {
			c.DefaultGroupPolicy.Review.BlockedWords = []string{"禁词"}
		},
	})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != "blocked" {
		t.Fatalf("task = %+v", task)
	}
	if strings.Contains(env.logs.String(), "禁词") {
		t.Errorf("blocked text leaked into logs")
	}
}

const testWindow = time.Minute

// withContinuation turns on sessions with short turns.
func withContinuation(c *config.Config) {
	c.DefaultGroupPolicy.Reply.ContinuationWindow = conloomapi.Duration{Duration: testWindow}
	c.DefaultGroupPolicy.Reply.TurnGap = conloomapi.Duration{Duration: 100 * time.Millisecond}
	c.DefaultGroupPolicy.Reply.TurnMaxWait = conloomapi.Duration{Duration: 400 * time.Millisecond}
}

func (e *testEnv) inContinuation(user string) bool {
	return e.o.inSession(testConvKey, user, time.Now(), e.cfg.DefaultGroupPolicy.Reply.ContinuationWindow.Duration)
}

// startContinuation has user mention the bot and waits until the bot's reply
// makes user a participant of the group's session.
func (e *testEnv) startContinuation(t *testing.T, user string) {
	t.Helper()
	task := e.waitTask(t, e.submit(t, e.groupEvent(user, "在吗", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("mention task = %+v", task)
	}
	waitUntil(t, "continuation of "+user, func() bool { return e.inContinuation(user) })
}

func (e *testEnv) counter(t *testing.T, name, label, value string) float64 {
	t.Helper()
	mfs, err := e.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func lastContent(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	m, _ := msgs[len(msgs)-1].(map[string]any)
	s, _ := m["content"].(string)
	return s
}

// turnDirective is the start of the hint appended to session turns.
const turnDirective = "【提示】你正在和 "

// skipOnDirective answers SkipMarker to requests carrying the turn directive
// and fakeReply otherwise.
func skipOnDirective(req map[string]any) (int, string) {
	if strings.Contains(lastContent(req), turnDirective) {
		return 200, SkipMarker
	}
	return 200, fakeReply
}

func TestRunner_ContinuationSkipSendsNothing(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: skipOnDirective, configure: withContinuation})
	env.startContinuation(t, "20001")
	if c := lastContent(env.short.Requests()[0]); strings.Contains(c, "【提示】") {
		t.Fatalf("mention request carries a directive: %q", c)
	}

	resp := env.submit(t, env.groupEvent("20001", "晚饭吃什么好呢"))
	if resp.Disposition != conloomapi.DispositionReply || resp.Merged {
		t.Fatalf("continuation response = %+v", resp)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Error != nil {
		t.Fatalf("skipped task = %+v", task)
	}
	if task.Meta.ReviewResult != "skipped" || task.Meta.Trigger != conloomapi.TriggerContinuation {
		t.Errorf("meta = %+v", task.Meta)
	}
	msgs := requestMessages(t, env.short.Requests()[1])
	last, _ := msgs[len(msgs)-1]["content"].(string)
	if !strings.HasSuffix(last, "晚饭吃什么好呢\n\n"+CopyDirectiveContinuation("用户20001")) {
		t.Errorf("continuation request current = %q", last)
	}
	first := requestMessages(t, env.short.Requests()[0])
	if sys, _ := msgs[0]["content"].(string); msgs[0]["role"] != "system" || sys != first[0]["content"] {
		t.Errorf("system prompt differs between mention and continuation requests")
	}
	if env.notifier.count(testBot) != 1 {
		t.Errorf("notifier called %d times, want 1 (the mention)", env.notifier.count(testBot))
	}
	if strings.Contains(env.logs.String(), SkipMarker) {
		t.Error("skip marker leaked into logs")
	}
}

func TestRunner_SkipMarkerAnywhereSendsNothing(t *testing.T) {
	for _, output := range []string{"好的[[SKIP]]", "[[SKIP]]\n其实我想说两句", "  [[SKIP]]  ", "<think>想想</think>嗯[[SKIP]]嗯"} {
		t.Run(output, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{short: answer(output), configure: withContinuation})
			task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
			if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != "skipped" {
				t.Fatalf("task = %+v", task)
			}
			if task.Meta.Trigger != conloomapi.TriggerMention {
				t.Errorf("trigger = %q", task.Meta.Trigger)
			}
			if n := env.notifier.count(testBot); n != 0 {
				t.Errorf("notifier called %d times", n)
			}
			time.Sleep(50 * time.Millisecond)
			if env.inContinuation("20001") {
				t.Error("a skipped reply opened a continuation window")
			}
			if n := env.counter(t, "conloom_continuation_exits_total", "reason", "model"); n != 0 {
				t.Errorf("model exits = %v for a mention task, want 0", n)
			}
			if strings.Contains(env.logs.String(), SkipMarker) {
				t.Error("skip marker leaked into logs")
			}
		})
	}
}

func TestRunner_SkipMarkerVariantsSendNothing(t *testing.T) {
	for _, output := range []string{
		"[[skip]]", "[[ SKIP ]]", "［［SKIP］］", "【【SKIP】】", "［［ｓｋｉｐ］］", "好的 [[ Skip ]] 吧",
		"SKIP", " skip\n", "[SKIP]", "【SKIP】", "［skip］", "[ S K I P ]",
		"SKIP。", "[SKIP]!", "SKIP\u200b", "\ufeff[SKIP]", "“SKIP”", "skip…", "【SKIP】。", "S\u200dKIP",
	} {
		t.Run(output, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{short: answer(output)})
			task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
			if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != "skipped" {
				t.Fatalf("task = %+v", task)
			}
		})
	}
	for _, output := range []string{"我不想 skip 这个话题", "SKIP 是跳过的意思", "[SKIP] 这个按钮", "好的。"} {
		t.Run(output, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{short: answer(output)})
			task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
			if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != output {
				t.Fatalf("task = %+v, want the output sent", task)
			}
		})
	}
}

func TestRunner_TriggerRecordedInMeta(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withContinuation})
	env.startContinuation(t, "20001")
	cont := env.waitTask(t, env.submit(t, env.groupEvent("20001", "接着聊")).TaskID)
	help := env.getTask(t, env.submit(t, env.groupEvent("20002", "/help")).TaskID)
	priv := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", "在吗")).TaskID)
	mention := env.waitTask(t, env.submit(t, env.groupEvent("20002", "你好", mentionBot)).TaskID)
	for _, c := range []struct {
		task conloomapi.Task
		want string
	}{
		{cont, conloomapi.TriggerContinuation},
		{help, conloomapi.TriggerCommand},
		{priv, conloomapi.TriggerPrivate},
		{mention, conloomapi.TriggerMention},
	} {
		if c.task.Status != conloomapi.TaskSucceeded || c.task.Meta.Trigger != c.want || c.task.Meta.TraceID == "" {
			t.Errorf("task %s: status %s meta %+v, want trigger %q", c.task.TaskID, c.task.Status, c.task.Meta, c.want)
		}
	}
}

func TestRunner_ContinuationMergedWithMentionHasNoDirective(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: skipOnDirective, configure: func(c *config.Config) {
		withContinuation(c)
		c.DefaultGroupPolicy.Reply.TurnGap = conloomapi.Duration{Duration: 500 * time.Millisecond}
		c.DefaultGroupPolicy.Reply.TurnMaxWait = conloomapi.Duration{Duration: time.Second}
	}})
	env.startContinuation(t, "20001")
	first := env.submit(t, env.groupEvent("20001", "对了"))
	second := env.submit(t, env.groupEvent("20001", "你怎么看", mentionBot))
	if !second.Merged || second.TaskID != first.TaskID {
		t.Fatalf("mention not merged into continuation task: %+v / %+v", first, second)
	}
	task := env.waitTask(t, first.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v", task)
	}
	if c := lastContent(env.short.Requests()[1]); strings.Contains(c, "【提示】") {
		t.Errorf("request for a task addressed to the bot carries the directive: %q", c)
	}
}

func TestRunner_ConcurrencyTwoBothReply(t *testing.T) {
	var inflight, peak atomic.Int32
	both := make(chan struct{})
	var once sync.Once
	handler := func(map[string]any) (int, string) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= 2 {
			once.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(2 * time.Second):
		}
		return 200, "收到"
	}
	env := newTestOrchestrator(t, testOpts{short: handler, configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Concurrency.MaxInteractiveRequests = 2
	}})
	ev1 := env.groupEvent("20001", "问题一", mentionBot)
	ev2 := env.groupEvent("20002", "问题二", mentionBot)
	r1, r2 := env.submit(t, ev1), env.submit(t, ev2)
	// The first reply quotes its trigger because 20002 spoke after it.
	for _, c := range []struct {
		taskID, msgID string
	}{{r1.TaskID, ev1.Message.ID}, {r2.TaskID, ""}} {
		task := env.waitTask(t, c.taskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].ReplyToMessageID != c.msgID {
			t.Fatalf("task %s = %+v", c.taskID, task)
		}
	}
	if p := peak.Load(); p != 2 {
		t.Fatalf("peak concurrent model calls = %d, want 2", p)
	}
}

func TestRunner_EchoPrefixOnlyOutputIsNotSent(t *testing.T) {
	echo := answer("[18:37] 小明：")
	env := newTestOrchestrator(t, testOpts{short: echo, long: echo})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskFailed || task.Error == nil || task.Error.Code != conloomapi.CodeModelUnavailable {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != CopyUnavailable {
		t.Fatalf("actions = %+v", task.Actions)
	}
	if s, l := env.short.Calls(), env.long.Calls(); s != 1 || l != 1 {
		t.Errorf("calls short=%d long=%d, want 1 and 1", s, l)
	}
	if strings.Contains(env.logs.String(), "[18:37]") {
		t.Error("model output leaked into logs")
	}
}

func TestRunner_EchoPrefixStrippedFromReply(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: answer("[18:37] 织语：你好呀")})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != "你好呀" {
		t.Fatalf("task = %+v", task)
	}
}

func TestRunner_TurnEchoOnlyOutputSendsNothing(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withContinuation, short: func(req map[string]any) (int, string) {
		if strings.Contains(lastContent(req), turnDirective) {
			return 200, "[18:37] 用户20001："
		}
		return 200, fakeReply
	}})
	env.startContinuation(t, "20001")
	resp := env.submit(t, env.groupEvent("20001", "晚饭吃什么好呢"))
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("continuation response = %+v", resp)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Error != nil {
		t.Fatalf("task = %+v", task)
	}
	if task.Meta.Trigger != conloomapi.TriggerContinuation {
		t.Errorf("meta = %+v", task.Meta)
	}
	if env.long.Calls() != 0 {
		t.Errorf("long calls = %d, want 0 (a turn is not re-routed)", env.long.Calls())
	}
	if strings.Contains(env.logs.String(), "[18:37]") {
		t.Error("model output leaked into logs")
	}
}

func TestRunner_TurnBareSpeakerHeaderSendsNothing(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withContinuation, short: func(req map[string]any) (int, string) {
		if strings.Contains(lastContent(req), turnDirective) {
			return 200, "[22:56] 小红"
		}
		return 200, fakeReply
	}})
	env.submit(t, env.groupEvent("20002", "晚上好", func(ev *conloomapi.Event) { ev.Actor.DisplayName = "小红" }))
	env.startContinuation(t, "20001")
	resp := env.submit(t, env.groupEvent("20001", "晚饭吃什么好呢"))
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("continuation response = %+v", resp)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Error != nil {
		t.Fatalf("task = %+v", task)
	}
	if strings.Contains(env.logs.String(), "[22:56]") {
		t.Error("model output leaked into logs")
	}
}

func TestRunner_PassesReplyTargetsForHistory(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	cold := env.groupEvent("20002", "今天好冷")
	env.submit(t, cold)
	env.submit(t, env.groupEvent("20003", "是啊", replyTo(cold.Message.ID)))
	env.submit(t, env.groupEvent("20003", "早就说了", replyTo("msg_gone")))
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你怎么看", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	var all strings.Builder
	for _, m := range requestMessages(t, env.short.Requests()[0]) {
		c, _ := m["content"].(string)
		all.WriteString(c + "\n")
	}
	for _, want := range []string{"用户20003（回复 用户20002 ", "的「今天好冷」）：是啊", "用户20003（回复了一条更早的消息）：早就说了"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("request lacks %q:\n%s", want, all.String())
		}
	}
}

// storeRecord stores m (with its stored fields defaulted) directly.
func (e *testEnv) storeRecord(t *testing.T, m store.MessageRecord) store.MessageRecord {
	t.Helper()
	now := time.Now().UTC()
	m.BotInstanceID, m.Platform, m.OccurredAt, m.CreatedAt = testBot, "qq", now, now
	if m.ConversationKey == "" {
		m.ConversationKey = testConvKey
	}
	_, err := e.st.Ingest(context.Background(), store.Ingest{
		Event:   store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "test:" + m.ID, EventID: "evt_" + m.ID, Response: []byte("{}"), CreatedAt: now},
		Message: &m,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

const otherConvKey = "bot_main:qq:group:group_10002"

// storeForeign stores a message of another group of the bot.
func (e *testEnv) storeForeign(t *testing.T, id string, fromBot bool, text string) {
	t.Helper()
	m := store.MessageRecord{ID: id, ConversationKey: otherConvKey, Text: text, FromBot: fromBot}
	if !fromBot {
		m.PlatformUserID, m.DisplayName = "20009", "外人"
	}
	e.storeRecord(t, m)
}

func requestText(t *testing.T, req map[string]any) string {
	t.Helper()
	var all strings.Builder
	for _, m := range requestMessages(t, req) {
		c, _ := m["content"].(string)
		all.WriteString(c + "\n")
	}
	return all.String()
}

func TestRunner_ReplyTargetFromOtherConversationIsUnknown(t *testing.T) {
	if store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "group_10002") != otherConvKey {
		t.Fatal("otherConvKey does not match the store's key format")
	}
	env := newTestOrchestrator(t, testOpts{})
	env.storeForeign(t, "msg_9001", false, "别处的秘密")
	env.storeForeign(t, "msg_9002", true, "别处的回答")
	env.submit(t, env.groupEvent("20003", "是啊", replyTo("msg_9001")))
	ev := env.groupEvent("20001", "你怎么看", mentionBot, replyTo("msg_9002"))
	task := env.waitTask(t, env.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	all := requestText(t, env.short.Requests()[0])
	for _, want := range []string{"用户20003（回复了一条更早的消息）：是啊", "【当前消息】发送者：用户20001\n（回复了一条更早的消息）\n你怎么看"} {
		if !strings.Contains(all, want) {
			t.Errorf("request lacks %q:\n%s", want, all)
		}
	}
	for _, leak := range []string{"别处的秘密", "别处的回答", "外人"} {
		if strings.Contains(all, leak) {
			t.Errorf("request leaks %q from another conversation:\n%s", leak, all)
		}
	}
}

func TestSubmit_ReplyToBotInOtherConversationIsNotAddressed(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.storeForeign(t, "msg_9002", true, "别处的回答")
	if resp := env.submit(t, env.groupEvent("20001", "对吗", replyTo("msg_9002"))); resp.Disposition != conloomapi.DispositionObserve {
		t.Fatalf("response = %+v, want observe (the target is not the bot's message here)", resp)
	}
	own := env.storeRecord(t, store.MessageRecord{ID: "msg_bot_own", Text: "我来啦", FromBot: true})
	if resp := env.submit(t, env.groupEvent("20001", "对吗", replyTo(own.ID))); resp.Disposition != conloomapi.DispositionReply {
		t.Fatalf("response = %+v, want reply to the bot's message in this group", resp)
	}
}

func TestRunner_ContinuationReplyToForeignBotKeepsDirective(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: skipOnDirective, configure: withContinuation})
	env.storeForeign(t, "msg_9002", true, "别处的回答")
	env.startContinuation(t, "20001")
	resp := env.submit(t, env.groupEvent("20001", "对吗", replyTo("msg_9002")))
	if resp.Disposition != conloomapi.DispositionReply {
		t.Fatalf("response = %+v", resp)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Meta.Trigger != conloomapi.TriggerContinuation || len(task.Actions) != 0 || task.Meta.ReviewResult != "skipped" {
		t.Fatalf("task = %+v, want a skipped continuation turn", task)
	}
	if c := lastContent(env.short.Requests()[1]); !strings.Contains(c, turnDirective) {
		t.Errorf("turn request lacks the directive: %q", c)
	}
}

func TestRunner_ReplyTargetsSkipBotHistory(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.submit(t, env.groupEvent("20002", "在吗"))
	env.storeRecord(t, store.MessageRecord{ID: "msg_bot_1", Text: "织语：在的", FromBot: true, ReplyTo: "msg_quoted_by_bot"})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	if env.hooks.lookedUp("msg_quoted_by_bot") {
		t.Error("looked up the reply target of a bot history message")
	}
	msgs := requestMessages(t, env.short.Requests()[0])
	var assistant []string
	for _, m := range msgs {
		if m["role"] == "assistant" {
			c, _ := m["content"].(string)
			assistant = append(assistant, c)
		}
	}
	if len(assistant) != 1 || assistant[0] != "在的" {
		t.Errorf("assistant history = %q, want [在的] (bot name stripped)", assistant)
	}
}

func TestRunner_EchoPrefixWithKnownSpeakerName(t *testing.T) {
	named := func(name string) evOpt { return func(ev *conloomapi.Event) { ev.Actor.DisplayName = name } }
	for _, c := range []struct{ output, want string }{
		{"[18:37] Mr.X：", CopyUnavailable},
		{"[18:37] ？？？：你好呀", "你好呀"},
	} {
		t.Run(c.output, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{short: answer(c.output), long: answer(c.output)})
			env.submit(t, env.groupEvent("20002", "在吗", named("Mr.X")))
			task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot, named("？？？"))).TaskID)
			if len(task.Actions) != 1 || task.Actions[0].Content.Text != c.want {
				t.Fatalf("task = %+v, want one action %q", task, c.want)
			}
		})
	}
}
