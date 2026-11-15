package core

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const scopeSummaryReply = `{"group_summary":"小明在两段聊天里提到下周三考试。","members":[]}`

// scopeLong answers consolidation requests with scopeSummaryReply and
// anything else with fakeReply.
func scopeLong(req map[string]any) (int, string) {
	msgs, _ := req["messages"].([]any)
	if len(msgs) > 0 {
		if m, _ := msgs[0].(map[string]any); strings.Contains(m["content"].(string), "记忆整理员") {
			return 200, scopeSummaryReply
		}
	}
	return 200, fakeReply
}

// panelCommand sends text in the panel chat convKey and returns the text of
// the command's single reply.
func (e *testEnv) panelCommand(t *testing.T, convKey, text string) string {
	t.Helper()
	resp := e.panelResponse(t, e.submitPanel(t, convKey, "admin", text))
	task := e.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("panel command %q: task %+v", text, task)
	}
	return task.Actions[0].Content.Text
}

// panelReply sends text in the panel chat convKey, waits for the reply and
// its affinity bookkeeping, and returns the last user message the reply model
// got.
func (e *testEnv) panelReply(t *testing.T, convKey, text string) string {
	t.Helper()
	resp := e.panelResponse(t, e.submitPanel(t, convKey, "admin", text))
	if task := e.injectDone(t, resp.TaskID); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("reply to %q: task %+v", text, task)
	}
	reqs := e.short.Requests()
	return lastContent(reqs[len(reqs)-1])
}

func TestScope_PanelChatsShareEverything(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true), long: scopeLong})
	ctx := context.Background()
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	scope := memory.ScopeKeyOf(a)
	if scope != testBot+":panel:private:~shared" || memory.ScopeKeyOf(b) != scope ||
		convType(scope) != conloomapi.ConvPrivate || convPlatform(scope) != store.PlatformPanel {
		t.Fatalf("scope of %s and %s = %q, %q", a, b, scope, memory.ScopeKeyOf(b))
	}
	const user = "panel_admin"
	scoped := func() store.MemberMemory { return env.memberIn(t, scope, user) }

	if got := env.panelCommand(t, a, "/记住 我下周三考试"); got != CopyRemembered {
		t.Fatalf("/记住 in A = %q", got)
	}
	if m := scoped(); !strings.Contains(m.Note, memory.RememberMarker+"我下周三考试") {
		t.Fatalf("scope member after /记住 in A = %+v", m)
	}

	inB := env.panelReply(t, b, "在吗")
	if !strings.Contains(inB, memory.RememberMarker+"我下周三考试") {
		t.Errorf("reply in B lacks the note remembered in A:\n%s", inB)
	}
	if m := scoped(); m.Affinity == nil || *m.Affinity != memory.InitialAffinity {
		t.Errorf("scope member after the reply in B = %+v, want affinity %d", m, memory.InitialAffinity)
	}
	if got, want := env.panelCommand(t, a, "/好感度"), CopyAffinity("织语", memory.TierName(memory.InitialAffinity), memory.InitialAffinity); got != want {
		t.Errorf("/好感度 in A = %q, want the affinity B's reply set: %q", got, want)
	}
	for _, key := range []string{a, b} {
		if rows, err := env.st.ListMemberMemories(ctx, key); err != nil || len(rows) != 0 {
			t.Errorf("member rows under chat %s = %+v, %v, want none", key, rows, err)
		}
	}

	inA := env.panelReply(t, a, "今天天气不错")
	if strings.Contains(inA, "在吗") {
		t.Errorf("reply in A shows B's history:\n%s", inA)
	}

	cons := memory.NewConsolidator(memory.ConsolidatorDeps{Store: env.st, Router: env.o.router, Admission: env.o.adm,
		Personas: env.o.personas, Policies: env.policies, Config: env.cfg, Bots: env.o.bots})
	if res, err := cons.Run(ctx, b, true); err != nil || res.Batches != 1 {
		t.Fatalf("consolidate the scope = %+v, %v", res, err)
	}
	var material string
	for _, req := range env.long.Requests() {
		if c := lastContent(req); strings.Contains(c, memory.LabelBatch) {
			material = c
		}
	}
	for _, want := range []string{"在吗", "今天天气不错", "我下周三考试", "〔对话1〕", "〔对话2〕"} {
		if !strings.Contains(material, want) {
			t.Errorf("consolidation material lacks %q:\n%s", want, material)
		}
	}
	for _, key := range []string{a, b} {
		if got, want := env.panelCommand(t, key, "/记忆 摘要"), CopyMemoryPrivateSummary("小明在两段聊天里提到下周三考试。"); got != want {
			t.Errorf("/记忆 摘要 in %s = %q, want %q", key, got, want)
		}
	}

	view := func(key string) conloomapi.MemoryState {
		t.Helper()
		conv, err := env.st.GetConversationByKey(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		state, err := env.o.memAdmin.View(ctx, conv)
		if err != nil {
			t.Fatal(err)
		}
		state.ConversationID = ""
		return state
	}
	va, vb := view(a), view(b)
	if va.Summary == "" || len(va.Members) != 1 || va.Members[0].UserID != user {
		t.Errorf("memory view of A = %+v", va)
	}
	if !reflect.DeepEqual(va, vb) {
		t.Errorf("memory views differ:\nA %+v\nB %+v", va, vb)
	}

	if got := env.panelCommand(t, b, "/忘记我"); got != CopyMemoryForgottenPrivate {
		t.Fatalf("/忘记我 in B = %q", got)
	}
	for c, want := range map[string]string{"/记忆": CopyMemoryNoNote, "/好感度": CopyAffinityNone("织语"),
		"/记忆 摘要": CopyMemoryNoPrivateSummary} {
		if got := env.panelCommand(t, a, c); got != want {
			t.Errorf("%s in A after /忘记我 in B = %q, want %q", c, got, want)
		}
	}
	if m := scoped(); m.ForgottenAt.IsZero() || m.Note != "" || m.Affinity != nil {
		t.Errorf("scope member after /忘记我 = %+v", m)
	}
	if again := env.panelReply(t, a, "还记得吗"); strings.Contains(again, "今天天气不错") || strings.Contains(again, "下周三考试") {
		t.Errorf("reply in A after /忘记我 in B shows what was said before:\n%s", again)
	}
}
