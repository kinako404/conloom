package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const injectBlockHeader = "【你记得的事"

// injectMorning is 11:00 on 2026-11-12 in Asia/Shanghai, the test server's
// timezone.
var injectMorning = time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)

// injectConfig turns memory on for the default group policy, with affinity
// rules as given.
func injectConfig(affinity bool) func(*config.Config) {
	return func(c *config.Config) {
		c.DefaultGroupPolicy.Memory = conloomapi.DefaultMemoryPolicy()
		c.DefaultGroupPolicy.Memory.Enabled = true
		c.DefaultGroupPolicy.Memory.Affinity = affinity
	}
}

// injectClock is a settable orchestrator clock; stamp gives events its time.
type injectClock struct{ ns atomic.Int64 }

func newInjectClock(at time.Time) *injectClock {
	c := &injectClock{}
	c.set(at)
	return c
}

func (c *injectClock) set(at time.Time)           { c.ns.Store(at.UnixNano()) }
func (c *injectClock) now() time.Time             { return time.Unix(0, c.ns.Load()).UTC() }
func (c *injectClock) stamp(ev *conloomapi.Event) { ev.OccurredAt = c.now() }

func injectAffinity(v int) *int { return &v }

// injectMembers returns the test group's member memory rows.
func (e *testEnv) injectMembers(t *testing.T) []store.MemberMemory {
	t.Helper()
	rows, err := e.st.ListMemberMemories(context.Background(), testConvKey)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// injectSeed stores the summary of convKey (when not empty) and its members.
func (e *testEnv) injectSeed(t *testing.T, convKey, summary string, members ...store.MemberMemory) {
	t.Helper()
	ctx := context.Background()
	if summary != "" {
		if err := e.st.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: convKey, Summary: summary}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range members {
		m.ConversationKey = convKey
		conflicts, err := e.st.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: convKey, Members: []store.MemberMemory{m}})
		if err != nil || len(conflicts) != 0 {
			t.Fatalf("seed member %s: %v, %v", m.PlatformUserID, conflicts, err)
		}
	}
}

// injectDone waits until taskID is finished and has logged so, which happens
// after its interaction was recorded.
func (e *testEnv) injectDone(t *testing.T, taskID string) conloomapi.Task {
	t.Helper()
	task := e.waitTask(t, taskID)
	waitUntil(t, "the finished log of "+taskID, func() bool {
		return strings.Contains(e.logs.String(), `msg="task finished" task_id=`+taskID+" ")
	})
	return task
}

// injectMember returns userID's memory row in the test group.
func (e *testEnv) injectMember(t *testing.T, userID string) (store.MemberMemory, bool) {
	t.Helper()
	got, err := e.st.GetMemberMemories(context.Background(), testConvKey, []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got[userID]
	return m, ok
}

// injectFailStore fails every member memory read.
type injectFailStore struct{ store.Store }

func (s *injectFailStore) GetMemberMemories(context.Context, string, []string) (map[string]store.MemberMemory, error) {
	return nil, errors.New("test: member memories unavailable")
}

func TestMemoryInject_BlockBeforeRecentMessages(t *testing.T) {
	run := func(t *testing.T, on bool) map[string]any {
		clock := newInjectClock(injectMorning)
		opts := testOpts{now: clock.now}
		if on {
			opts.configure = injectConfig(true)
		}
		env := newTestOrchestrator(t, opts)
		env.injectSeed(t, testConvKey, "最近在聊猫",
			store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(52)},
			store.MemberMemory{PlatformUserID: "20002", Note: "养了狗"})
		red := env.groupEvent("20002", "我家狗会握手了", withName("小红"), clock.stamp)
		env.submit(t, red)
		ev := env.groupEvent("20001", "真厉害", withName("小明"), clock.stamp, replyTo(red.Message.ID),
			atUser("user_20002", "小红"), mentionBot)
		task := env.waitTask(t, env.submit(t, ev).TaskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("task = %+v", task)
		}
		return env.short.Requests()[0]
	}
	on, off := run(t, true), run(t, false)

	block := memory.RenderBlock("最近在聊猫", []memory.BlockEntry{
		{Name: "小明", Tier: "熟悉", Note: "喜欢猫"},
		{Name: "小红", Note: "养了狗"},
	}, 0)
	last := lastContent(on)
	if !strings.HasPrefix(last, block+"\n\n【近期消息") {
		t.Fatalf("last user message = %q, want it to start with the block and then the recent messages", last)
	}
	for _, want := range []string{"小明（熟悉）：喜欢猫", "小红：养了狗"} {
		if !strings.Contains(last, want) {
			t.Errorf("last user message lacks %q", want)
		}
	}
	for _, leak := range []string{"52", "20001", "20002"} {
		if strings.Contains(last, leak) {
			t.Errorf("last user message shows %q:\n%s", leak, last)
		}
	}
	if offLast := lastContent(off); strings.Contains(offLast, injectBlockHeader) || !strings.HasSuffix(last, offLast) {
		t.Errorf("without memory the message = %q, want it to be the memory message without the block", offLast)
	}
	onMsgs, offMsgs := requestMessages(t, on), requestMessages(t, off)
	if onMsgs[0]["role"] != "system" || onMsgs[0]["content"] != offMsgs[0]["content"] {
		t.Errorf("system message changed with memory on:\n%v\nvs\n%v", onMsgs[0]["content"], offMsgs[0]["content"])
	}
	if len(onMsgs) != len(offMsgs) {
		t.Errorf("memory on sends %d messages, off %d", len(onMsgs), len(offMsgs))
	}
}

func TestMemoryInject_AtMostTwoOthers(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	env.injectSeed(t, testConvKey, "",
		store.MemberMemory{PlatformUserID: "20001", Note: "笔记一"},
		store.MemberMemory{PlatformUserID: "20002", Note: "笔记二"},
		store.MemberMemory{PlatformUserID: "20003", Note: "笔记三"},
		store.MemberMemory{PlatformUserID: "29999", Note: "笔记九"})
	env.submit(t, env.groupEvent("20002", "早", withName("小红")))
	env.submit(t, env.groupEvent("20003", "早"))
	env.submit(t, env.groupEvent("29999", "早"))
	ev := env.groupEvent("20001", "大家都在", withName("小明"), mentionBot,
		atUser("user_20002", ""), atUser("user_20003", ""), atUser("user_20002", ""), atUser("user_29999", ""))
	task := env.waitTask(t, env.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	last := lastContent(env.short.Requests()[0])
	block, _, _ := strings.Cut(last, "\n\n")
	for _, want := range []string{"\n小明：笔记一\n", "\n小红：笔记二\n", "\n用户20003：笔记三\n"} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}
	if strings.Contains(last, "笔记九") {
		t.Errorf("block shows a third other member:\n%s", block)
	}
}

func TestMemoryInject_DisabledNoBlock(t *testing.T) {
	for _, c := range []struct {
		name      string
		configure func(*config.Config)
	}{
		{"disabled", nil},
		{"long requests off", func(c *config.Config) {
			injectConfig(true)(c)
			c.DefaultGroupPolicy.Concurrency.AllowLongRequests = false
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: c.configure})
			env.injectSeed(t, testConvKey, "最近在聊猫",
				store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(52)})
			task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
			if task.Status != conloomapi.TaskSucceeded {
				t.Fatalf("task = %+v", task)
			}
			if all := requestText(t, env.short.Requests()[0]); strings.Contains(all, injectBlockHeader) || strings.Contains(all, "喜欢猫") {
				t.Fatalf("request carries memory:\n%s", all)
			}
		})
	}
}

func TestMemoryInject_NothingRememberedNoBlock(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	env.injectSeed(t, testConvKey, "", store.MemberMemory{PlatformUserID: "20001", Affinity: injectAffinity(52)})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	if last := lastContent(env.short.Requests()[0]); strings.Contains(last, injectBlockHeader) || strings.Contains(last, "熟悉") {
		t.Fatalf("request carries a memory block:\n%s", last)
	}
}

func TestMemoryInject_ReadErrorStillReplies(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	env.injectSeed(t, testConvKey, "最近在聊猫",
		store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(52)})
	env.o.st = &injectFailStore{Store: env.o.st}
	task := env.injectDone(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
	if all := requestText(t, env.short.Requests()[0]); strings.Contains(all, injectBlockHeader) {
		t.Fatalf("request carries a memory block:\n%s", all)
	}
	if logs := env.logs.String(); !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "member memories unavailable") {
		t.Errorf("read error not logged as a warning:\n%s", logs)
	}
}

func TestAffinity_FirstDirectReplySets20(t *testing.T) {
	clock := newInjectClock(injectMorning)
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true), now: clock.now})
	task := env.injectDone(t, env.submit(t, env.groupEvent("20001", "你好", withName("小明"), clock.stamp, mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	m, ok := env.injectMember(t, "20001")
	if !ok || m.Affinity == nil || *m.Affinity != 20 || m.LastInteractionDay != "2026-11-12" || m.DisplayName != "小明" {
		t.Fatalf("member = %+v (affinity %v), want affinity 20 on 2026-11-12 named 小明", m, m.Affinity)
	}
	revs, err := env.st.ListMemoryRevisions(context.Background(), testConvKey, "20001")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 || revs[0].Source != store.RevisionRule || revs[0].Reason != memory.ReasonFirstInteraction ||
		revs[0].Field != store.FieldAffinity || revs[0].OldValue != "" || revs[0].NewValue != "20" {
		t.Fatalf("revisions = %+v, want one rule revision '' -> 20", revs)
	}
	if n := env.counter(t, "conloom_memory_affinity_changes_total", "source", store.RevisionRule); n != 1 {
		t.Errorf("rule affinity changes = %v, want 1", n)
	}
}

func TestAffinity_DailyPlusOneOncePerLocalDay(t *testing.T) {
	clock := newInjectClock(injectMorning)
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true), now: clock.now})
	mention := func() {
		t.Helper()
		task := env.injectDone(t, env.submit(t, env.groupEvent("20001", "你好", withName("小明"), clock.stamp, mentionBot)).TaskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("task = %+v", task)
		}
	}
	affinity := func(wantDay string) int {
		t.Helper()
		m, ok := env.injectMember(t, "20001")
		if !ok || m.Affinity == nil || m.LastInteractionDay != wantDay {
			t.Fatalf("member = %+v, want an affinity last raised on %s", m, wantDay)
		}
		return *m.Affinity
	}

	mention()
	clock.set(injectMorning.Add(4 * time.Hour))
	mention()
	if a := affinity("2026-11-12"); a != 20 {
		t.Fatalf("affinity after two replies on one day = %d, want 20", a)
	}
	clock.set(time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))
	mention()
	if a := affinity("2026-11-13"); a != 21 {
		t.Fatalf("affinity on the next local day = %d, want 21", a)
	}
	revs, err := env.st.ListMemoryRevisions(context.Background(), testConvKey, "20001")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 || revs[1].Reason != memory.ReasonDaily || revs[1].OldValue != "20" || revs[1].NewValue != "21" {
		t.Fatalf("revisions = %+v, want first_interaction then daily 20 -> 21", revs)
	}
	if n := env.counter(t, "conloom_memory_affinity_changes_total", "source", store.RevisionRule); n != 2 {
		t.Errorf("rule affinity changes = %v, want 2", n)
	}
}

func TestAffinity_NoChangeOnFailureTurnOrJoin(t *testing.T) {
	t.Run("model failure", func(t *testing.T) {
		fail := func(map[string]any) (int, string) { return 500, "boom" }
		env := newTestOrchestrator(t, testOpts{short: fail, long: fail, configure: injectConfig(true)})
		task := env.injectDone(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
		if task.Status != conloomapi.TaskFailed || len(task.Actions) != 1 {
			t.Fatalf("task = %+v, want failed with the unavailable copy", task)
		}
		if rows := env.injectMembers(t); len(rows) != 0 {
			t.Fatalf("member rows = %+v, want none", rows)
		}
	})
	t.Run("unaddressed turn", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
			withContinuation(c)
			injectConfig(true)(c)
		}})
		env.o.startJoinSession(testConvKey, []string{"20001"}, time.Now())
		task := env.turn(t, "20001", "今天好冷")
		env.injectDone(t, task.TaskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("turn = %+v, want a sent reply", task)
		}
		if rows := env.injectMembers(t); len(rows) != 0 {
			t.Fatalf("member rows = %+v, want none", rows)
		}
	})
	t.Run("join", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: joinConfig(time.Hour, injectConfig(true))})
		env.seedMember(t, "20001", conloomapi.ProactiveLow, time.Now().Add(-time.Hour))
		env.submit(t, env.groupEvent("20001", matchText))
		env.chat(t, line{"20002", "好的"}, line{"20003", "行"})
		task := env.requireJoin(t)
		env.injectDone(t, task.TaskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("join = %+v, want a sent reply", task)
		}
		if rows := env.injectMembers(t); len(rows) != 0 {
			t.Fatalf("member rows = %+v, want none", rows)
		}
	})
}

func TestAffinity_OffOrDisabledNoChange(t *testing.T) {
	for _, c := range []struct {
		name      string
		configure func(*config.Config)
	}{
		{"affinity off", injectConfig(false)},
		{"memory disabled", func(c *config.Config) {
			injectConfig(true)(c)
			c.DefaultGroupPolicy.Memory.Enabled = false
		}},
		{"long requests off", func(c *config.Config) {
			injectConfig(true)(c)
			c.DefaultGroupPolicy.Concurrency.AllowLongRequests = false
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: c.configure})
			task := env.injectDone(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
			if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
				t.Fatalf("task = %+v", task)
			}
			if rows := env.injectMembers(t); len(rows) != 0 {
				t.Fatalf("member rows = %+v, want none", rows)
			}
		})
	}
}

func TestMemoryInject_BlockCappedToTheReplyBudget(t *testing.T) {
	clock := newInjectClock(injectMorning)
	env := newTestOrchestrator(t, testOpts{now: clock.now, configure: injectConfig(true)})
	summary := strings.Repeat("很早以前在聊天气。", 300) + "最新约好周六去看展。"
	env.injectSeed(t, testConvKey, summary,
		store.MemberMemory{PlatformUserID: "20001", Note: strings.Repeat("喜欢猫", 60), Affinity: injectAffinity(52)})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "周末去哪", withName("小明"), clock.stamp, mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || env.short.Calls() != 1 || env.long.Calls() != 0 {
		t.Fatalf("task = %+v, short calls %d, long calls %d; want the reply on the short model", task, env.short.Calls(), env.long.Calls())
	}
	block, _, ok := strings.Cut(lastContent(env.short.Requests()[0]), "\n\n【当前消息")
	if !ok || !strings.HasPrefix(block, injectBlockHeader) {
		t.Fatalf("last user message does not start with the block: %q", block)
	}
	limit := int(float64(env.cfg.ModelProfiles["short_context"].Context.SoftInputBudget) * injectBlockShare)
	if n := utf8.RuneCountInString(block); n > limit {
		t.Fatalf("block = %d runes, over %d", n, limit)
	}
	for _, want := range []string{"群里最近：…", "最新约好周六去看展。", "小明（熟悉）：" + strings.Repeat("喜欢猫", 60)} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}
}
