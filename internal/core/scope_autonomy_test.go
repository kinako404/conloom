package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/skills/tools"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// lateTool is a tool whose implementation is set once the test orchestrator,
// whose store it needs, exists.
type lateTool struct {
	spec  skills.Spec
	inner skills.Tool
}

func (l *lateTool) Spec() skills.Spec { return l.spec }

func (l *lateTool) Call(ctx context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	return l.inner.Call(ctx, inv, args)
}

// defaultPersonas gives every conversation the default persona.
type defaultPersonas struct{}

func (defaultPersonas) Persona(context.Context, string, string) (conloomapi.PersonaDoc, error) {
	return conloomapi.DefaultPersona(), nil
}

const (
	// goBathe and goodNews make scopeAutonomyModel call set_status and
	// adjust_buff.
	goBathe  = "我去洗澡了"
	goodNews = "告诉你个好消息"
)

// scopeAutonomyModel calls set_status (洗澡, 20 minutes) when the newest
// message of the current turn is goBathe and adjust_buff (心情 +5) when it is
// goodNews, and otherwise, or once a tool answered, replies fakeReply.
func scopeAutonomyModel(req map[string]any) fakeopenai.Reply {
	last := lastContent(req)
	newest := last[strings.LastIndex(last, "\n")+1:]
	switch {
	case hasToolResult(req) || !strings.Contains(last, "【当前消息】"):
	case newest == goBathe:
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "call_s", Name: "set_status",
			Arguments: `{"activity":"洗澡","minutes":20}`}}}
	case newest == goodNews:
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "call_b", Name: "adjust_buff",
			Arguments: `{"name":"心情","delta":5,"reason":"听到好消息"}`}}}
	}
	return fakeopenai.Reply{Content: fakeReply}
}

// scopeAutonomyEnv is a test orchestrator at injectMorning with memory on
// whose bot has the status and buff skills with the real set_status and
// adjust_buff tools, answered by scopeAutonomyModel.
func scopeAutonomyEnv(t *testing.T) (*testEnv, *injectClock) {
	t.Helper()
	clock := newInjectClock(injectMorning)
	status := &lateTool{spec: tools.NewSetStatus(nil, nil, nil, nil).Spec()}
	buff := &lateTool{spec: tools.NewAdjustBuff(nil, nil).Spec()}
	env := skillEnv(t, testOpts{shortTools: scopeAutonomyModel, now: clock.now, configure: privMemConfig(true)},
		[]string{"status", "buff"}, status, buff)
	status.inner = tools.NewSetStatus(env.st, env.policies, defaultPersonas{}, review.New(nil, review.Options{}))
	buff.inner = tools.NewAdjustBuff(env.st, defaultPersonas{})
	return env, clock
}

func (e *testEnv) scopeStatus(t *testing.T, scope string) (store.ConversationStatus, bool) {
	t.Helper()
	st, err := e.st.GetStatus(context.Background(), scope)
	if errors.Is(err, store.ErrNotFound) {
		return store.ConversationStatus{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return st, true
}

func (e *testEnv) timerKindsOf(t *testing.T, convKey string) []store.TimerKind {
	t.Helper()
	timers, err := e.st.ListTimers(context.Background(), convKey)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []store.TimerKind
	for _, tm := range timers {
		kinds = append(kinds, tm.Kind)
	}
	return kinds
}

func TestScope_StatusSharedReturnInOrigin(t *testing.T) {
	env, clock := scopeAutonomyEnv(t)
	ctx := context.Background()
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	scope := memory.ScopeKeyOf(a)

	started := clock.now()
	env.panelReply(t, a, goBathe)
	st, ok := env.scopeStatus(t, scope)
	until := started.Add(20 * time.Minute)
	if !ok || st.Activity != "洗澡" || st.Origin() != a || !st.UntilAt.Equal(until) {
		t.Fatalf("scope status after A's reply = %+v (found %v), want 洗澡 set in A until %v", st, ok, until)
	}
	if got := env.timerKindsOf(t, a); !slices.Equal(got, []store.TimerKind{store.TimerStatusReturn}) {
		t.Fatalf("timers of A = %v, want its status_return", got)
	}
	if got := env.timerKindsOf(t, b); len(got) != 0 {
		t.Fatalf("timers of B = %v, want none", got)
	}

	clock.set(started.Add(5 * time.Minute))
	inB := env.panelReply(t, b, "你在干嘛")
	block := autonomy.RenderState(&autonomy.StateStatus{Activity: "洗澡", StartedAt: started, UntilAt: until},
		defaultStateBars(60, 70), nil, time.Time{}, env.o.loc)
	if !strings.Contains(inB, block) {
		t.Fatalf("B's request lacks the status set in A:\n%s\nwant\n%s", inB, block)
	}
	view, err := env.o.Autonomy().State(ctx, b)
	if err != nil || view.Status == nil || view.Status.Activity != "洗澡" {
		t.Fatalf("B's autonomy view = %+v, %v; want the shared status", view, err)
	}

	requests := len(env.short.Requests())
	clock.set(until)
	env.o.pollWakes(ctx)
	timer := store.ConversationTimer{ConversationKey: a, Kind: store.TimerStatusReturn, DueAt: until}
	id, ok := env.wakeTaskID(t, timer)
	if !ok {
		t.Fatal("the status return stored no task in A")
	}
	task := env.waitTask(t, id)
	if len(task.Actions) != 1 || task.Meta.Trigger != TriggerStatusReturn {
		t.Fatalf("status return = %+v, want it sent", task)
	}
	wantChannels(t, "status return", env.actionChannels(t, task), store.ChannelPanel)
	waitUntil(t, "the status return's runner", func() bool { return !env.o.sched.Busy(a) })
	if rec, err := env.st.GetOutbox(ctx, testBot, task.Actions[0].ActionID); err != nil || rec.ConversationKey != a {
		t.Fatalf("status return action = %+v, %v; want it in A", rec, err)
	}
	if reqs := env.short.Requests(); len(reqs) != requests+1 || !strings.Contains(lastContent(reqs[len(reqs)-1]),
		CopyDirectiveStatusReturn("洗澡")) {
		t.Fatalf("model requests after the return = %d, want one more returning from 洗澡", len(reqs))
	}
	if st, ok := env.scopeStatus(t, scope); ok {
		t.Fatalf("scope status after the return = %+v, want it cleared", st)
	}

	env.panelReply(t, a, goBathe)
	if st, ok := env.scopeStatus(t, scope); !ok || st.Origin() != a {
		t.Fatalf("scope status after A's second reply = %+v (found %v)", st, ok)
	}
	c := env.createChat(t, testBot)
	if err := env.o.DeletePanelChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	if st, ok := env.scopeStatus(t, scope); !ok || st.Origin() != a {
		t.Fatalf("scope status after deleting another chat = %+v (found %v), want A's kept", st, ok)
	}
	if err := env.o.DeletePanelChat(ctx, a); err != nil {
		t.Fatal(err)
	}
	if st, ok := env.scopeStatus(t, scope); ok {
		t.Errorf("scope status after deleting A, where it was set = %+v, want it cleared", st)
	}
	if got := env.timerKindsOf(t, a); len(got) != 0 {
		t.Errorf("timers of the deleted A = %v", got)
	}
}

func TestScope_BarsShared(t *testing.T) {
	env, clock := scopeAutonomyEnv(t)
	ctx := context.Background()
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	scope := memory.ScopeKeyOf(a)

	env.panelReply(t, a, goodNews)
	inB := env.panelReply(t, b, "在吗")
	if block := autonomy.RenderState(nil, defaultStateBars(65, 70), nil, time.Time{}, env.o.loc); !strings.Contains(inB, block) {
		t.Fatalf("B's request lacks the bar A's reply changed:\n%s\nwant\n%s", inB, block)
	}
	env.panelReply(t, b, goodNews)
	if err := env.o.Autonomy().SetBar(ctx, b, "精力", 40, "测试", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	state := func(key string) conloomapi.AutonomyState {
		t.Helper()
		s, err := env.o.Autonomy().State(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	sa, sb := state(a), state(b)
	if len(sa.Bars) != 2 || sa.Bars[0].Value != 70 || sa.Bars[1].Value != 40 || len(sa.Events) != 3 {
		t.Fatalf("A's view = %+v, want 心情 70 and 精力 40 after three changes in A and B", sa)
	}
	if !slices.Equal(sa.Bars, sb.Bars) || !slices.Equal(sa.Events, sb.Events) {
		t.Errorf("bar views differ:\nA %+v %+v\nB %+v %+v", sa.Bars, sa.Events, sb.Bars, sb.Events)
	}
	for _, key := range []string{a, b} {
		if bars, err := env.st.GetBuffs(ctx, key); err != nil || len(bars) != 0 {
			t.Errorf("bars stored under the chat %s = %+v, %v", key, bars, err)
		}
	}

	clock.set(clock.now().Add(time.Minute))
	env.panelReply(t, b, goBathe)
	if _, ok := env.scopeStatus(t, scope); !ok {
		t.Fatal("no status after B's reply")
	}
	if got := env.panelCommand(t, a, "/忘记我"); got != CopyMemoryForgottenPrivate {
		t.Fatalf("/忘记我 in A = %q", got)
	}
	if st, ok := env.scopeStatus(t, scope); ok {
		t.Errorf("scope status after /忘记我 in A = %+v, want it cleared", st)
	}
	if bars, err := env.st.GetBuffs(ctx, scope); err != nil || len(bars) != 0 {
		t.Errorf("scope bars after /忘记我 = %+v, %v", bars, err)
	}
	if events, err := env.st.ListBuffEvents(ctx, scope, 10); err != nil || len(events) != 0 {
		t.Errorf("scope bar changes after /忘记我 = %+v, %v", events, err)
	}
	if got := env.timerKindsOf(t, b); len(got) != 0 {
		t.Errorf("timers of B after /忘记我 in A = %v, want its status_return gone", got)
	}
}

func TestScope_DeleteChatKeepsSharedMemory(t *testing.T) {
	env, _ := scopeAutonomyEnv(t)
	ctx := context.Background()
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	scope := memory.ScopeKeyOf(a)
	const user = "panel_admin"

	if got := env.panelCommand(t, a, "/记住 我喜欢猫"); got != CopyRemembered {
		t.Fatalf("/记住 in A = %q", got)
	}
	env.panelReply(t, a, goodNews)
	env.panelReply(t, b, goBathe)
	if err := env.st.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: scope, Summary: "小明喜欢猫", CursorSeq: 1}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{a, b} {
		msgs, err := env.st.RecentMessages(ctx, key, 1)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("messages of %s = %v, %v", key, msgs, err)
		}
		stamp, err := env.st.ForgetStamp(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		m := msgs[0]
		if ok, err := env.st.WriteChunks(ctx, store.ChunkWrite{ConversationKey: key, Model: "m1", ForgetStamp: stamp,
			Chunks: []store.MemoryChunk{{ConversationKey: key, StartSeq: m.Seq, EndSeq: m.Seq, StartAt: m.OccurredAt,
				EndAt: m.OccurredAt, TextHash: "h", Model: "m1", Vector: []float32{1, 0}}}}); err != nil || !ok {
			t.Fatalf("WriteChunks of %s = %v, %v", key, ok, err)
		}
	}

	if err := env.o.DeletePanelChat(ctx, a); err != nil {
		t.Fatal(err)
	}
	chunks := func(key string) int {
		t.Helper()
		cs, err := env.st.LoadChunks(ctx, key, "m1", 0)
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}
	if n := chunks(a); n != 0 {
		t.Errorf("chunks of the deleted A = %d, want none", n)
	}
	if n := chunks(b); n != 1 {
		t.Errorf("chunks of B = %d, want its own kept", n)
	}
	if m := env.memberIn(t, scope, user); !strings.Contains(m.Note, "我喜欢猫") || m.Affinity == nil {
		t.Errorf("scope member after deleting A = %+v, want the note and affinity kept", m)
	}
	if g, err := env.st.GetGroupMemory(ctx, scope); err != nil || g.Summary != "小明喜欢猫" {
		t.Errorf("scope summary after deleting A = %+v, %v", g, err)
	}
	if st, ok := env.scopeStatus(t, scope); !ok || st.Origin() != b {
		t.Errorf("scope status after deleting A = %+v (found %v), want B's kept", st, ok)
	}
	if got := env.timerKindsOf(t, b); !slices.Equal(got, []store.TimerKind{store.TimerStatusReturn}) {
		t.Errorf("timers of B = %v, want its status_return kept", got)
	}
	if bars, err := env.st.GetBuffs(ctx, scope); err != nil || len(bars) != 1 {
		t.Errorf("scope bars after deleting A = %+v, %v; want the change A's reply made", bars, err)
	}
	if inB := env.panelReply(t, b, "还记得吗"); !strings.Contains(inB, memory.RememberMarker+"我喜欢猫") {
		t.Errorf("B's reply after deleting A lacks the shared note:\n%s", inB)
	}
}

// scopeRows counts the rows under key in every table that keeps memory by
// conversation_key in the database at path.
func scopeRows(t *testing.T, path, key string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]int{}
	for _, table := range []string{"group_memory", "member_memory", "memory_revisions", "memory_chunk_forgets",
		"conversation_status", "conversation_buffs", "conversation_buff_events"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE conversation_key = ?`, key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func TestScope_DeleteBotClearsScope(t *testing.T) {
	env := panelBotEnv(t, testOpts{configure: privMemConfig(true)})
	ctx := context.Background()
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红"})
	other := env.createPanelBot(t, PanelBotInput{Name: "小绿"})
	a, b, kept := env.createChat(t, bot.ID), env.createChat(t, bot.ID), env.createChat(t, other.ID)
	scope, otherScope := memory.ScopeKeyOf(a), memory.ScopeKeyOf(kept)
	command := func(botID, key, text string) {
		t.Helper()
		if task := env.waitBotTask(t, botID, env.panelBotMessage(t, botID, key, text)); task.Status != conloomapi.TaskSucceeded {
			t.Fatalf("%s in %s: %+v", text, key, task)
		}
	}
	command(bot.ID, b, "/忘记我")
	command(bot.ID, a, "/记住 我喜欢猫")
	command(other.ID, kept, "/记住 我喜欢狗")
	if err := env.st.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: scope, Summary: "小明喜欢猫", CursorSeq: 1}); err != nil {
		t.Fatal(err)
	}
	until := injectMorning.Add(20 * time.Minute)
	if err := env.st.SetStatus(ctx, store.ConversationStatus{ConversationKey: scope, OriginConversationKey: b, Activity: "洗澡",
		StartedAt: injectMorning, UntilAt: until}, &store.ConversationTimer{BotInstanceID: bot.ID,
		PlatformUserID: PanelActorID("admin"), DueAt: until}); err != nil {
		t.Fatal(err)
	}
	if err := env.o.Autonomy().SetBar(ctx, a, "心情", 80, "测试", store.BuffSourcePanel, panelBotActor); err != nil {
		t.Fatal(err)
	}
	for table, n := range scopeRows(t, env.dbPath, scope) {
		if n == 0 {
			t.Fatalf("no %s rows in the bot's scope before the delete: %v", table, scopeRows(t, env.dbPath, scope))
		}
	}
	before := scopeRows(t, env.dbPath, otherScope)

	if err := env.o.PanelBots().Delete(ctx, bot.ID, "小红", panelBotActor); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for table, n := range scopeRows(t, env.dbPath, scope) {
		if n != 0 {
			t.Errorf("%s rows left in the deleted bot's scope: %d", table, n)
		}
	}
	for table := range rowsOfBot(t, env.dbPath, bot.ID) {
		if table != "bot_instances" && table != "model_calls" && table != "audit_events" {
			t.Errorf("rows of the deleted bot left in %s: %v", table, rowsOfBot(t, env.dbPath, bot.ID))
		}
	}
	if after := scopeRows(t, env.dbPath, otherScope); !maps.Equal(after, before) || after["member_memory"] != 1 {
		t.Errorf("other bot's scope = %v, want it untouched (%v)", after, before)
	}
}
