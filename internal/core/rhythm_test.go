package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const privateUser = "20001"

var privateConvKey = store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, conloomapi.ConvPrivate+"_"+privateUser)

// newPrivateEnv returns an orchestrator that answers everyone's private
// messages after a pause of wait, with model as its short model (nil answers
// fakeReply at once).
func newPrivateEnv(t *testing.T, wait time.Duration, model handlerFunc, configure func(*config.Config)) *testEnv {
	t.Helper()
	return newTestOrchestrator(t, testOpts{short: model, configure: func(c *config.Config) {
		c.Bots[0].PrivateChat = "all"
		c.DefaultGroupPolicy.Reply.PrivateWait = conloomapi.Duration{Duration: wait}
		c.DefaultGroupPolicy.Reply.PrivateMaxWait = conloomapi.Duration{Duration: 2 * time.Second}
		if configure != nil {
			configure(c)
		}
	}})
}

func (e *testEnv) privateEvent(text string) conloomapi.Event {
	return e.event(conloomapi.ConvPrivate, privateUser, privateUser, text)
}

// gatedModel answers fakeReply; its first call waits until open is called,
// which tests register as a cleanup after creating the orchestrator so that
// it runs before the model server closes.
type gatedModel struct {
	calls   atomic.Int32
	release chan struct{}
	once    sync.Once
}

func newGatedModel() *gatedModel {
	return &gatedModel{release: make(chan struct{})}
}

func (g *gatedModel) open() { g.once.Do(func() { close(g.release) }) }

func (g *gatedModel) handle(map[string]any) (int, string) {
	if g.calls.Add(1) == 1 {
		<-g.release
	}
	return 200, fakeReply
}

// lastUserMessage returns the content of the last message of the model
// request n (0-based).
func lastUserMessage(t *testing.T, env *testEnv, n int) string {
	t.Helper()
	reqs := env.short.Requests()
	if len(reqs) <= n {
		t.Fatalf("model requests = %d, want more than %d", len(reqs), n)
	}
	msgs := requestMessages(t, reqs[n])
	last := msgs[len(msgs)-1]
	content, _ := last["content"].(string)
	if last["role"] != "user" {
		t.Fatalf("last message of request %d = %v", n, last)
	}
	return content
}

func storedTriggers(t *testing.T, env *testEnv, taskID string) []string {
	t.Helper()
	rec, err := env.st.GetTask(context.Background(), testBot, taskID)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", taskID, err)
	}
	return rec.TriggerMessageIDs
}

func requireCancelledWithoutActions(t *testing.T, env *testEnv, taskID string) {
	t.Helper()
	task := env.waitTask(t, taskID)
	if task.Status != conloomapi.TaskCancelled || len(task.Actions) != 0 {
		t.Fatalf("task %s = %+v, want cancelled without actions", taskID, task)
	}
	if task.Error == nil || task.Error.Code != conloomapi.CodeTaskCancelled {
		t.Fatalf("task %s error = %+v, want %s", taskID, task.Error, conloomapi.CodeTaskCancelled)
	}
}

func requireOneReply(t *testing.T, env *testEnv, taskID string) conloomapi.Task {
	t.Helper()
	task := env.waitTask(t, taskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task %s = %+v, want one reply", taskID, task)
	}
	return task
}

func supersededAudits(t *testing.T, env *testEnv) []store.AuditEvent {
	t.Helper()
	events, err := env.st.ListAudit(context.Background(), store.AuditFilter{Kind: "outbox.superseded", BotInstanceID: testBot, Limit: 10})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return events
}

func TestPrivate_BurstWithinPauseIsOneTurn(t *testing.T) {
	env := newPrivateEnv(t, 250*time.Millisecond, nil, func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 1
	})
	texts := []string{"今天好累", "加班到现在", "想吃点甜的"}
	var ids []string
	var taskID string
	for i, text := range texts {
		if i > 0 {
			// Longer than the group debounce, shorter than the private wait.
			time.Sleep(80 * time.Millisecond)
		}
		ev := env.privateEvent(text)
		ids = append(ids, ev.Message.ID)
		resp := env.submit(t, ev)
		if resp.Disposition != conloomapi.DispositionReply {
			t.Fatalf("message %d: response %+v", i+1, resp)
		}
		if i == 0 {
			taskID = resp.TaskID
		} else if resp.TaskID != taskID || !resp.Merged {
			t.Fatalf("message %d: response %+v, want merged into %s", i+1, resp, taskID)
		}
	}
	requireOneReply(t, env, taskID)
	time.Sleep(50 * time.Millisecond)
	if n := env.short.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
	content := lastUserMessage(t, env, 0)
	for _, text := range texts {
		if !strings.Contains(content, text) {
			t.Fatalf("current message %q lacks %q", content, text)
		}
	}
	if got := storedTriggers(t, env, taskID); !slices.Equal(got, ids) {
		t.Fatalf("stored triggers = %v, want %v", got, ids)
	}
	// The turn spent the only rate-limit hit: a new turn is refused.
	requireObserved(t, env, env.privateEvent("还在吗"), conloomapi.SuppressedRateLimited)
}

func TestPrivate_FollowUpDuringGenerationRestartsTurn(t *testing.T) {
	model := newGatedModel()
	env := newPrivateEnv(t, 30*time.Millisecond, model.handle, func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 1
	})
	t.Cleanup(model.open)
	ev1 := env.privateEvent("讲个故事吧")
	first := env.submit(t, ev1)
	waitUntil(t, "model called", func() bool { return env.short.Calls() == 1 })

	ev2 := env.privateEvent("要关于猫的")
	second := env.submit(t, ev2)
	if second.Disposition != conloomapi.DispositionReply || second.TaskID == "" || second.TaskID == first.TaskID {
		t.Fatalf("follow-up response = %+v (first %+v)", second, first)
	}
	requireCancelledWithoutActions(t, env, first.TaskID)
	requireOneReply(t, env, second.TaskID)
	if got, want := storedTriggers(t, env, second.TaskID), []string{ev1.Message.ID, ev2.Message.ID}; !slices.Equal(got, want) {
		t.Fatalf("new turn triggers = %v, want %v", got, want)
	}
	content := lastUserMessage(t, env, 1)
	if !strings.Contains(content, "讲个故事吧") || !strings.Contains(content, "要关于猫的") {
		t.Fatalf("restarted turn's current message = %q, want both messages", content)
	}
	if got := env.hooks.completed(first.TaskID); !slices.Contains(got, conloomapi.TaskCancelled) || slices.Contains(got, conloomapi.TaskSucceeded) {
		t.Fatalf("superseded task completions = %v", got)
	}
}

// splitReply stores parts 2 and 3 of taskID's reply, after its first part,
// due a minute from now.
func splitReply(t *testing.T, env *testEnv, taskID string) (part2, part3 string) {
	t.Helper()
	ctx := context.Background()
	task := env.getTask(t, taskID)
	first, err := env.st.GetOutbox(ctx, testBot, task.Actions[0].ActionID)
	if err != nil {
		t.Fatal(err)
	}
	var parts []store.OutboxRecord
	for i, text := range []string{"第二段", "第三段"} {
		p := first
		p.Seq = 0
		p.Action.ActionID = fmt.Sprintf("act_part%d_%s", i+2, taskID)
		p.Action.Content.Text = text
		p.NotBefore = time.Now().Add(time.Minute).UTC()
		parts = append(parts, p)
	}
	err = env.st.CompleteTask(ctx, store.TaskCompletion{TaskID: taskID, Status: task.Status, Meta: task.Meta, Actions: parts})
	if err != nil {
		t.Fatal(err)
	}
	return parts[0].Action.ActionID, parts[1].Action.ActionID
}

// deliverFirstPart leases the conversation's first action and acks it as
// sent, storing it as the bot's message.
func deliverFirstPart(t *testing.T, env *testEnv, want string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	leased, err := env.st.LeaseOutbox(ctx, testBot, 10, now, time.Minute)
	if err != nil || len(leased) != 1 || leased[0].Action.ActionID != want {
		t.Fatalf("leased = %+v, %v; want %s", leased, err, want)
	}
	a := leased[0]
	botMsg := &store.MessageRecord{
		ID: "msg_bot_" + want, BotInstanceID: testBot, ConversationKey: a.ConversationKey, Platform: a.Platform,
		PlatformMessageID: "p_" + want, Text: a.Action.Content.Text, FromBot: true, OccurredAt: now, CreatedAt: now,
	}
	if _, err := env.st.AckOutbox(ctx, testBot, want, conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "p_" + want}, botMsg, now); err != nil {
		t.Fatal(err)
	}
}

func outboxStatusOf(t *testing.T, env *testEnv, actionID string) string {
	t.Helper()
	rec, err := env.st.GetOutbox(context.Background(), testBot, actionID)
	if err != nil {
		t.Fatalf("GetOutbox(%s): %v", actionID, err)
	}
	return rec.Status
}

func TestPrivate_FollowUpCancelsUnsentParts(t *testing.T) {
	env := newPrivateEnv(t, 30*time.Millisecond, nil, nil)
	first := env.submit(t, env.privateEvent("推荐一本书"))
	reply := requireOneReply(t, env, first.TaskID)
	part2, part3 := splitReply(t, env, first.TaskID)
	deliverFirstPart(t, env, reply.Actions[0].ActionID)

	ev := env.privateEvent("换一本吧")
	second := env.submit(t, ev)
	if second.Disposition != conloomapi.DispositionReply || second.TaskID == first.TaskID || second.Merged {
		t.Fatalf("follow-up response = %+v", second)
	}
	for _, id := range []string{part2, part3} {
		if st := outboxStatusOf(t, env, id); st != store.OutboxCancelled {
			t.Fatalf("part %s = %s, want cancelled", id, st)
		}
	}
	if st := outboxStatusOf(t, env, reply.Actions[0].ActionID); st != store.OutboxAcked {
		t.Fatalf("sent part = %s, want acked", st)
	}
	audits := supersededAudits(t, env)
	if len(audits) != 1 {
		t.Fatalf("outbox.superseded audits = %+v, want 1", audits)
	}
	var detail struct {
		TaskID    string `json:"task_id"`
		Cancelled int    `json:"cancelled"`
	}
	if err := json.Unmarshal([]byte(audits[0].Detail), &detail); err != nil || detail.TaskID != first.TaskID || detail.Cancelled != 2 {
		t.Fatalf("audit detail = %s (%v), want task %s cancelled 2", audits[0].Detail, err, first.TaskID)
	}

	requireOneReply(t, env, second.TaskID)
	if got := storedTriggers(t, env, second.TaskID); !slices.Equal(got, []string{ev.Message.ID}) {
		t.Fatalf("new turn triggers = %v, want only the follow-up", got)
	}
	if content := lastUserMessage(t, env, 1); !strings.Contains(content, "换一本吧") || strings.Contains(content, "推荐一本书") {
		t.Fatalf("new turn's current message = %q, want only the follow-up", content)
	}
	b, err := json.Marshal(env.short.Requests()[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), fakeReply) || strings.Contains(string(b), "第二段") {
		t.Fatalf("new turn's context = %s, want the sent part only", b)
	}
}

func TestPrivate_FollowUpWhileReplyIsStoredStartsNewTurn(t *testing.T) {
	env := newPrivateEnv(t, 30*time.Millisecond, nil, nil)
	entered, release := env.blockFirstCompletion(t, func(c store.TaskCompletion) bool { return len(c.Actions) > 0 })
	ev1 := env.privateEvent("早上好")
	first := env.submit(t, ev1)
	if id := waitEntered(t, entered); id != first.TaskID {
		t.Fatalf("paused completion of %s, want %s", id, first.TaskID)
	}

	ev2 := env.privateEvent("今天天气怎样")
	body, err := json.Marshal(ev2)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		resp conloomapi.EventAccepted
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, _, err := env.o.Submit(context.Background(), testBot, "test:"+ev2.Message.ID, body, ev2)
		done <- result{resp, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("follow-up admitted while the reply was being stored: %+v, %v", r.resp, r.err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	r := <-done
	if r.err != nil || r.resp.Disposition != conloomapi.DispositionReply || r.resp.TaskID == first.TaskID {
		t.Fatalf("follow-up response = %+v, %v", r.resp, r.err)
	}

	reply := requireOneReply(t, env, first.TaskID)
	requireOneReply(t, env, r.resp.TaskID)
	if got := env.hooks.completed(first.TaskID); !slices.Equal(got, []string{conloomapi.TaskSucceeded}) {
		t.Fatalf("first task completions = %v, want [succeeded]", got)
	}
	if got := storedTriggers(t, env, first.TaskID); !slices.Equal(got, []string{ev1.Message.ID}) {
		t.Fatalf("first task triggers = %v", got)
	}
	if got := storedTriggers(t, env, r.resp.TaskID); !slices.Equal(got, []string{ev2.Message.ID}) {
		t.Fatalf("new turn triggers = %v, want only the follow-up", got)
	}
	// None of the first reply went out yet: it is kept whole, so the first
	// message is still answered.
	if st := outboxStatusOf(t, env, reply.Actions[0].ActionID); st != store.OutboxPending {
		t.Fatalf("first reply = %s, want pending", st)
	}
	if audits := supersededAudits(t, env); len(audits) != 0 {
		t.Fatalf("audits = %+v, want none", audits)
	}
}

func TestPrivate_FollowUpBeforeReplyIsStoredSupersedesIt(t *testing.T) {
	var armed atomic.Bool
	reached, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	env := newTestOrchestrator(t, testOpts{
		short: func(map[string]any) (int, string) {
			if calls.Add(1) == 1 {
				armed.Store(true)
			}
			return 200, fakeReply
		},
		// The first clock read after the first model answer is the runner's
		// on its way to storing the reply: it waits there for the follow-up.
		now: func() time.Time {
			if armed.CompareAndSwap(true, false) {
				close(reached)
				<-proceed
			}
			return time.Now()
		},
		configure: func(c *config.Config) { c.Bots[0].PrivateChat = "all" },
	})
	t.Cleanup(func() { once.Do(func() { close(proceed) }) })
	ev1 := env.privateEvent("帮我想个名字")
	first := env.submit(t, ev1)
	select {
	case <-reached:
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting for the runner to store its reply")
	}
	ev2 := env.privateEvent("给猫起的")
	second := env.submit(t, ev2)
	once.Do(func() { close(proceed) })
	if second.Disposition != conloomapi.DispositionReply || second.TaskID == first.TaskID {
		t.Fatalf("follow-up response = %+v", second)
	}
	requireCancelledWithoutActions(t, env, first.TaskID)
	requireOneReply(t, env, second.TaskID)
	if got, want := storedTriggers(t, env, second.TaskID), []string{ev1.Message.ID, ev2.Message.ID}; !slices.Equal(got, want) {
		t.Fatalf("new turn triggers = %v, want %v", got, want)
	}
	waitUntil(t, "superseded runner to finish", func() bool { return len(env.o.sched.Active(privateConvKey)) == 0 })
	if task := env.getTask(t, first.TaskID); task.Status != conloomapi.TaskCancelled || len(task.Actions) != 0 {
		t.Fatalf("superseded task after its runner finished = %+v", task)
	}
}

func TestPrivate_CommandsNeitherMergeNorCancel(t *testing.T) {
	model := newGatedModel()
	env := newPrivateEnv(t, 200*time.Millisecond, model.handle, nil)
	t.Cleanup(model.open)
	admin := func(text string) conloomapi.Event {
		return env.event(conloomapi.ConvPrivate, "29999", "29999", text)
	}
	adminKey := store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, conloomapi.ConvPrivate+"_29999")
	ev := admin("讲个笑话")
	turn := env.submit(t, ev)
	help := func() {
		t.Helper()
		resp := env.submit(t, admin("/help"))
		if resp.TaskID == turn.TaskID || resp.Merged {
			t.Fatalf("/help response = %+v", resp)
		}
		task := env.getTask(t, resp.TaskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != CopyHelp("织语") {
			t.Fatalf("/help task = %+v", task)
		}
	}
	help()
	active := env.o.sched.Active(adminKey)
	if len(active) != 1 || active[0].TaskID != turn.TaskID || !slices.Equal(active[0].TriggerMessageIDs, []string{ev.Message.ID}) {
		t.Fatalf("active after /help while waiting = %+v", active)
	}
	waitUntil(t, "model called", func() bool { return env.short.Calls() == 1 })
	help()
	if task := env.getTask(t, turn.TaskID); task.Status != conloomapi.TaskRunning {
		t.Fatalf("turn after /help while running = %+v", task)
	}
	model.open()
	requireOneReply(t, env, turn.TaskID)
	if got := storedTriggers(t, env, turn.TaskID); !slices.Equal(got, []string{ev.Message.ID}) {
		t.Fatalf("turn triggers = %v", got)
	}
	content := lastUserMessage(t, env, 0)
	if _, current, ok := strings.Cut(content, "【当前消息】"); !ok || strings.Contains(current, "/help") {
		t.Fatalf("turn's current message = %q, want no command", content)
	}
}

func TestGroup_MentionDuringRunningTaskDoesNotRestart(t *testing.T) {
	model := newGatedModel()
	env := newTestOrchestrator(t, testOpts{short: model.handle})
	t.Cleanup(model.open)
	first := env.submit(t, env.groupEvent("20001", "讲个故事", mentionBot))
	waitUntil(t, "model called", func() bool { return env.short.Calls() == 1 })
	second := env.submit(t, env.groupEvent("20001", "再讲一个", mentionBot))
	if second.TaskID == first.TaskID || second.Merged {
		t.Fatalf("second mention = %+v", second)
	}
	if task := env.getTask(t, first.TaskID); task.Status != conloomapi.TaskRunning {
		t.Fatalf("running task after a second mention = %+v", task)
	}
	model.open()
	requireOneReply(t, env, first.TaskID)
	requireOneReply(t, env, second.TaskID)
	if got := storedTriggers(t, env, second.TaskID); len(got) != 1 {
		t.Fatalf("second task triggers = %v", got)
	}
}

var interruptOpts = MergeOptions{Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second, Interrupt: true}

func TestScheduler_InterruptSupersedesRunningTask(t *testing.T) {
	rec := newSchedRecorder()
	rec.hold = make(chan struct{})
	t.Cleanup(func() { close(rec.hold) })
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: time.Hour}))

	r1, err := s.AdmitWith("private:20001", "20001", "m1", AdmitNew, interruptOpts, nopPersist)
	if err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t)
	var plans []AdmitPlan
	r2, err := s.AdmitWith("private:20001", "20001", "m2", AdmitNew, interruptOpts, func(p AdmitPlan) error {
		plans = append(plans, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []AdmitPlan{{NewTaskID: r2.TaskID, Supersedes: r1.TaskID}}; !slices.Equal(plans, want) || !r2.Created || r2.TaskID == r1.TaskID {
		t.Fatalf("plans = %+v, result %+v; want %+v", plans, r2, want)
	}
	runs := rec.waitFinished(t, 1)
	if runs[0].task.TaskID != r1.TaskID || runs[0].ctxErr == nil {
		t.Fatalf("first run = %+v, want %s cancelled", runs[0], r1.TaskID)
	}
	next := rec.waitStarted(t)
	if next.TaskID != r2.TaskID || !slices.Equal(next.TriggerMessageIDs, []string{"m1", "m2"}) {
		t.Fatalf("next started = %+v, want %s with [m1 m2]", next, r2.TaskID)
	}
	// Superseding needs Interrupt.
	r3, err := s.AdmitWith("private:20001", "20001", "m3", AdmitNew, MergeOptions{}, func(p AdmitPlan) error {
		if p.Supersedes != "" {
			t.Errorf("plan without Interrupt = %+v", p)
		}
		return nil
	})
	if err != nil || !r3.Created {
		t.Fatalf("admit without Interrupt = %+v, %v", r3, err)
	}
	if active := s.Active("private:20001"); len(active) != 2 || active[0].TaskID != r2.TaskID {
		t.Fatalf("active = %+v, want %s still running", active, r2.TaskID)
	}
}

func TestScheduler_InterruptRefusedCreatesPlainTask(t *testing.T) {
	rec := newSchedRecorder()
	rec.hold = make(chan struct{})
	t.Cleanup(func() { close(rec.hold) })
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10}))

	r1, err := s.AdmitWith("private:20001", "20001", "m1", AdmitNew, interruptOpts, nopPersist)
	if err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t)
	var plans []AdmitPlan
	r2, err := s.AdmitWith("private:20001", "20001", "m2", AdmitNew, interruptOpts, func(p AdmitPlan) error {
		plans = append(plans, p)
		if p.Supersedes != "" {
			return ErrSupersedeRefused
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []AdmitPlan{{NewTaskID: r2.TaskID, Supersedes: r1.TaskID}, {NewTaskID: r2.TaskID}}
	if !slices.Equal(plans, want) || !r2.Created {
		t.Fatalf("plans = %+v, result %+v; want %+v", plans, r2, want)
	}
	// The refused task is not offered again; the new task takes the merge.
	plans = nil
	r3, err := s.AdmitWith("private:20001", "20001", "m3", AdmitNew, interruptOpts, func(p AdmitPlan) error {
		plans = append(plans, p)
		return nil
	})
	if err != nil || !r3.Merged || r3.TaskID != r2.TaskID || !slices.Equal(plans, []AdmitPlan{{MergeIntoTaskID: r2.TaskID}}) {
		t.Fatalf("third admit = %+v, %v, plans %+v", r3, err, plans)
	}
	active := s.Active("private:20001")
	if len(active) != 2 || active[0].TaskID != r1.TaskID || !slices.Equal(active[1].TriggerMessageIDs, []string{"m2", "m3"}) {
		t.Fatalf("active = %+v", active)
	}
	if len(rec.finished()) != 0 {
		t.Fatalf("a run finished: %+v", rec.finished())
	}

	// A failing persist neither supersedes nor creates.
	if _, err := s.AdmitWith("private:20002", "20002", "n1", AdmitNew, interruptOpts, nopPersist); err != nil {
		t.Fatal(err)
	}
	if started := rec.waitStarted(t); started.ActorID != "20002" {
		t.Fatalf("started = %+v, want 20002's task", started)
	}
	boom := errors.New("boom")
	if _, err := s.AdmitWith("private:20002", "20002", "n2", AdmitNew, interruptOpts, func(AdmitPlan) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("failing persist err = %v", err)
	}
	if active := s.Active("private:20002"); len(active) != 1 {
		t.Fatalf("active after failed admit = %+v", active)
	}
}

func TestScheduler_InterruptMergesIntoTaskPastReadyAt(t *testing.T) {
	rec := newSchedRecorder()
	rec.hold = make(chan struct{})
	t.Cleanup(func() { close(rec.hold) })
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10}))

	// Another actor's task holds the only slot, so 20001's task stays queued
	// past its ReadyAt.
	if _, err := s.AdmitWith("conv", "20002", "x1", AdmitNew, interruptOpts, nopPersist); err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t)
	r1, err := s.AdmitWith("conv", "20001", "m1", AdmitNew, interruptOpts, nopPersist)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * interruptOpts.Debounce)
	r2, err := s.AdmitWith("conv", "20001", "m2", AdmitNew, interruptOpts, nopPersist)
	if err != nil || !r2.Merged || r2.TaskID != r1.TaskID {
		t.Fatalf("admit past ReadyAt = %+v, %v; want merged into %s", r2, err, r1.TaskID)
	}
	time.Sleep(2 * interruptOpts.Debounce)
	plain, err := s.AdmitWith("conv", "20001", "m3", AdmitNew, MergeOptions{Debounce: 20 * time.Millisecond}, nopPersist)
	if err != nil || plain.Merged {
		t.Fatalf("admit without Interrupt = %+v, %v; want a new task", plain, err)
	}
}

func TestScheduler_InterruptZeroDebounceStartsAtOnce(t *testing.T) {
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: time.Hour, MaxMergeWindow: time.Hour}))
	if _, err := s.AdmitWith("private:20001", "20001", "m1", AdmitNew, MergeOptions{MaxMergeWindow: time.Second, Interrupt: true}, nopPersist); err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t)
}
