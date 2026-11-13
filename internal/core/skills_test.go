package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// recTool is a fake tool that records its invocations.
type recTool struct {
	name string
	fn   func(args json.RawMessage) (string, error)

	mu    sync.Mutex
	invs  []skills.Invocation
	args  []string
	calls int
}

func (r *recTool) Spec() skills.Spec {
	return skills.Spec{Name: r.name, Description: "fake " + r.name, Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
}

func (r *recTool) Call(_ context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	r.mu.Lock()
	r.invs = append(r.invs, inv)
	r.args = append(r.args, string(args))
	r.calls++
	r.mu.Unlock()
	if r.fn != nil {
		return r.fn(args)
	}
	return "2026-11-13 星期五 16:30（Asia/Shanghai）", nil
}

func (r *recTool) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

const nowResult = "2026-11-13 星期五 16:30（Asia/Shanghai）"

// skillEnv is a test orchestrator whose bot enables botSkills. Besides the
// built-in skills it knows privnote (private chats only, tool now), chaos
// (tool boom) and verbose (tool long).
func skillEnv(t *testing.T, opts testOpts, botSkills []string, tools ...skills.Tool) *testEnv {
	t.Helper()
	loaded, err := skills.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []skills.Skill{
		{Name: "privnote", Description: "d", Tools: []string{"now"}, Conversations: []string{"private"}, Body: "- 私聊里用 now。"},
		{Name: "chaos", Description: "d", Tools: []string{"boom"}, Conversations: []string{"private", "group"}, Body: "- 用 boom。"},
		{Name: "verbose", Description: "d", Tools: []string{"long"}, Conversations: []string{"private", "group"}, Body: "- 用 long。"},
	} {
		loaded[s.Name] = s
	}
	reg, err := skills.NewRegistry(loaded, tools)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Check(botSkills); err != nil {
		t.Fatal(err)
	}
	opts.skills = reg
	configure := opts.configure
	opts.configure = func(c *config.Config) {
		c.Bots[0].Skills = botSkills
		if configure != nil {
			configure(c)
		}
	}
	return newTestOrchestrator(t, opts)
}

func offeredTools(req map[string]any) []string {
	var names []string
	tools, _ := req["tools"].([]any)
	for _, tool := range tools {
		fn, _ := tool.(map[string]any)["function"].(map[string]any)
		name, _ := fn["name"].(string)
		names = append(names, name)
	}
	return names
}

func hasToolResult(req map[string]any) bool {
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if m.(map[string]any)["role"] == "tool" {
			return true
		}
	}
	return false
}

// toolMessages returns the tool messages of req in order.
func toolMessages(req map[string]any) []map[string]any {
	var out []map[string]any
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm := m.(map[string]any); mm["role"] == "tool" {
			out = append(out, mm)
		}
	}
	return out
}

func (e *testEnv) skillCount(t *testing.T, tool, result string) float64 {
	t.Helper()
	mfs, err := e.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "conloom_skill_calls_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["tool"] == tool && labels["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// callOnceThen answers the first request of a task with calls and every
// request carrying tool results with final.
func callOnceThen(final string, calls ...fakeopenai.ToolCall) func(map[string]any) fakeopenai.Reply {
	return func(req map[string]any) fakeopenai.Reply {
		if hasToolResult(req) {
			return fakeopenai.Reply{Content: final}
		}
		return fakeopenai.Reply{ToolCalls: calls}
	}
}

func TestSkills_DisabledRequestsUnchanged(t *testing.T) {
	plain := newTestOrchestrator(t, testOpts{})
	unused := skillEnv(t, testOpts{}, nil, &recTool{name: "now"})
	var tasks []conloomapi.Task
	for _, env := range []*testEnv{plain, unused} {
		tasks = append(tasks, env.waitTask(t, env.submit(t, env.groupEvent("20001", "今天几号", withName("小明"), mentionBot)).TaskID))
	}
	a, b := plain.short.Requests()[0], unused.short.Requests()[0]
	if !reflect.DeepEqual(a, b) {
		t.Errorf("request with a registry but no enabled skills differs:\n%v\n%v", a, b)
	}
	if _, ok := b["tools"]; ok || strings.Contains(requestText(t, b), "【技能】") {
		t.Errorf("request offers skills: %v", b)
	}
	if !reflect.DeepEqual(tasks[0].Actions[0].Content, tasks[1].Actions[0].Content) {
		t.Errorf("outputs differ: %+v / %+v", tasks[0].Actions, tasks[1].Actions)
	}
}

func TestSkills_OnlyDirectConversations(t *testing.T) {
	now := &recTool{name: "now"}
	env := skillEnv(t, testOpts{configure: withContinuation, shortTools: func(map[string]any) fakeopenai.Reply {
		return fakeopenai.Reply{Content: fakeReply}
	}}, []string{"time"}, now)
	env.startContinuation(t, "20001")
	if got := offeredTools(env.short.Requests()[0]); !reflect.DeepEqual(got, []string{"now"}) {
		t.Fatalf("mention request tools = %v, want [now]", got)
	}
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "晚饭吃什么好呢")).TaskID)
	if task.Meta.Trigger != conloomapi.TriggerContinuation {
		t.Fatalf("task = %+v", task)
	}
	turn := env.short.Requests()[1]
	if _, ok := turn["tools"]; ok || strings.Contains(requestText(t, turn), "【技能】") {
		t.Errorf("turn request offers skills: %v", turn)
	}
}

func TestSkills_ConversationTypes(t *testing.T) {
	now := &recTool{name: "now"}
	env := skillEnv(t, testOpts{shortTools: func(map[string]any) fakeopenai.Reply { return fakeopenai.Reply{Content: fakeReply} }},
		[]string{"privnote"}, now)
	env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
	if got := offeredTools(env.short.Requests()[0]); got != nil {
		t.Errorf("group request tools = %v, want none for a private-only skill", got)
	}
	env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", "几点了")).TaskID)
	req := env.short.Requests()[1]
	if got := offeredTools(req); !reflect.DeepEqual(got, []string{"now"}) {
		t.Errorf("private request tools = %v, want [now]", got)
	}
	if sys := requestMessages(t, req)[0]["content"].(string); !strings.HasSuffix(sys, "\n\n【技能】你可以在需要时调用下面的工具；不需要时直接回复。\n- 私聊里用 now。") {
		t.Errorf("private system prompt ends %q", sys[max(0, len(sys)-120):])
	}
}

func TestSkills_SingleRound(t *testing.T) {
	now := &recTool{name: "now"}
	env := skillEnv(t, testOpts{shortTools: callOnceThen("现在是下午四点半。", fakeopenai.ToolCall{ID: "call_1", Name: "now", Arguments: "{}"})},
		[]string{"time"}, now)
	ev := env.groupEvent("20001", "现在几点", withName("小明"), mentionBot)
	task := env.waitTask(t, env.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != "现在是下午四点半。" {
		t.Fatalf("task = %+v", task)
	}
	reqs := env.short.Requests()
	if len(reqs) != 2 || now.count() != 1 {
		t.Fatalf("requests = %d, tool calls = %d; want 2 and 1", len(reqs), now.count())
	}
	first := requestMessages(t, reqs[0])
	if sys := first[0]["content"].(string); !strings.HasSuffix(sys, "\n\n【技能】你可以在需要时调用下面的工具；不需要时直接回复。\n"+
		"- 用户问今天几号、星期几、现在几点，或者回答需要知道\"今天\"时，调用 now。\n- 用你自己的口吻自然地说出来，不要照抄格式。") {
		t.Errorf("system prompt lacks the skill section: %q", sys)
	}
	if _, ok := reqs[0]["tool_choice"]; ok {
		t.Errorf("first request sets tool_choice: %v", reqs[0]["tool_choice"])
	}
	second := requestMessages(t, reqs[1])
	if len(second) != len(first)+2 || !reflect.DeepEqual(second[:len(first)], first) {
		t.Fatalf("second request does not extend the first: %v", second)
	}
	call, result := second[len(first)], second[len(first)+1]
	wantCall := map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
		"id": "call_1", "type": "function", "function": map[string]any{"name": "now", "arguments": "{}"}}}}
	if !reflect.DeepEqual(call, wantCall) {
		t.Errorf("assistant message = %v, want %v", call, wantCall)
	}
	if want := map[string]any{"role": "tool", "content": nowResult, "tool_call_id": "call_1"}; !reflect.DeepEqual(result, want) {
		t.Errorf("tool message = %v, want %v", result, want)
	}
	if n := env.skillCount(t, "now", "ok"); n != 1 {
		t.Errorf("skill calls now/ok = %v, want 1", n)
	}
	logs := env.logs.String()
	if !strings.Contains(logs, `msg="skill call"`) || !strings.Contains(logs, "tool=now") || !strings.Contains(logs, "skill_calls=now:1") {
		t.Errorf("logs lack the skill call: %s", logs)
	}
	if strings.Contains(logs, "16:30") {
		t.Error("tool result leaked into logs")
	}
}

func TestSkills_IntermediateContentNotSent(t *testing.T) {
	now := &recTool{name: "now"}
	env := skillEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
		if hasToolResult(req) {
			return fakeopenai.Reply{Content: "好的"}
		}
		return fakeopenai.Reply{Content: "我查一下时间", ToolCalls: []fakeopenai.ToolCall{{ID: "c", Name: "now", Arguments: "{}"}}}
	}}, []string{"time"}, now)
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != "好的" {
		t.Fatalf("actions = %+v, want only the final reply", task.Actions)
	}
	msgs := requestMessages(t, env.short.Requests()[1])
	if c := msgs[len(msgs)-2]["content"]; c != "我查一下时间" {
		t.Errorf("assistant content sent back to the model = %v", c)
	}
}

func TestSkills_RoundLimitForcesText(t *testing.T) {
	for _, final := range []fakeopenai.Reply{
		{Content: "说不上来"},
		{Content: "说不上来", ToolCalls: []fakeopenai.ToolCall{{ID: "x", Name: "now", Arguments: `{"again":true}`}}},
	} {
		now := &recTool{name: "now"}
		round := 0
		var mu sync.Mutex
		env := skillEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
			if req["tool_choice"] == "none" {
				return final
			}
			mu.Lock()
			defer mu.Unlock()
			round++
			return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: fmt.Sprintf("r%d", round), Name: "now", Arguments: fmt.Sprintf(`{"round":%d}`, round)}}}
		}}, []string{"time"}, now)
		task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
		if len(task.Actions) != 1 || task.Actions[0].Content.Text != "说不上来" {
			t.Fatalf("task = %+v", task)
		}
		reqs := env.short.Requests()
		if len(reqs) != 4 || now.count() != 3 {
			t.Fatalf("requests = %d, tool calls = %d; want 4 and 3", len(reqs), now.count())
		}
		for i, req := range reqs {
			choice, _ := req["tool_choice"].(string)
			if want := map[bool]string{true: "none", false: ""}[i == 3]; choice != want || len(offeredTools(req)) != 1 {
				t.Errorf("request %d: tool_choice %q, tools %v", i, choice, offeredTools(req))
			}
		}
		if n := len(toolMessages(reqs[3])); n != 3 {
			t.Errorf("last request carries %d tool results, want 3", n)
		}
	}
}

func TestSkills_AtMostFourCallsPerRound(t *testing.T) {
	now := &recTool{name: "now"}
	var calls []fakeopenai.ToolCall
	for i := range 6 {
		calls = append(calls, fakeopenai.ToolCall{ID: fmt.Sprintf("c%d", i), Name: "now", Arguments: fmt.Sprintf(`{"i":%d}`, i)})
	}
	env := skillEnv(t, testOpts{shortTools: callOnceThen("好", calls...)}, []string{"time"}, now)
	env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
	results := toolMessages(env.short.Requests()[1])
	if len(results) != 6 || now.count() != 4 {
		t.Fatalf("tool messages = %d, executed = %d; want 6 and 4", len(results), now.count())
	}
	for i, m := range results {
		want := nowResult
		if i >= 4 {
			want = "本轮调用太多。"
		}
		if m["content"] != want || m["tool_call_id"] != fmt.Sprintf("c%d", i) {
			t.Errorf("tool message %d = %v, want %q for c%d", i, m, want, i)
		}
	}
	if !reflect.DeepEqual(now.args[:2], []string{`{"i":0}`, `{"i":1}`}) {
		t.Errorf("tool args = %v", now.args)
	}
}

func TestSkills_FailuresReachOnlyTheModel(t *testing.T) {
	now := &recTool{name: "now"}
	boom := &recTool{name: "boom", fn: func(json.RawMessage) (string, error) {
		return "", errors.New("database is down: secret detail")
	}}
	env := skillEnv(t, testOpts{shortTools: callOnceThen("好的，我知道了。",
		fakeopenai.ToolCall{ID: "a", Name: "launch_rocket", Arguments: "{}"},
		fakeopenai.ToolCall{ID: "b", Name: "now", Arguments: "{bad json"},
		fakeopenai.ToolCall{ID: "c", Name: "now", Arguments: "[1]"},
		fakeopenai.ToolCall{ID: "d", Name: "boom", Arguments: "{}"},
	)}, []string{"time", "chaos"}, now, boom)
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "试试", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != "好的，我知道了。" {
		t.Fatalf("task = %+v", task)
	}
	for i, m := range toolMessages(env.short.Requests()[1]) {
		if m["content"] != "调用失败。" {
			t.Errorf("tool message %d = %v, want 调用失败。", i, m)
		}
	}
	if now.count() != 0 || boom.count() != 1 {
		t.Errorf("executed now %d, boom %d; want 0 and 1", now.count(), boom.count())
	}
	for _, c := range []struct{ tool, result string }{{"unknown", "unknown"}, {"now", "invalid"}, {"boom", "error"}} {
		want := 1.0
		if c.result == "invalid" {
			want = 2
		}
		if n := env.skillCount(t, c.tool, c.result); n != want {
			t.Errorf("skill calls %s/%s = %v, want %v", c.tool, c.result, n, want)
		}
	}
	if logs := env.logs.String(); strings.Contains(logs, "secret detail") || strings.Contains(logs, "launch_rocket") {
		t.Errorf("logs leak tool details: %s", logs)
	}
}

func TestSkills_ResultTruncated(t *testing.T) {
	long := &recTool{name: "long", fn: func(json.RawMessage) (string, error) { return strings.Repeat("长", 2500), nil }}
	env := skillEnv(t, testOpts{shortTools: callOnceThen("好", fakeopenai.ToolCall{ID: "a", Name: "long", Arguments: ""})},
		[]string{"verbose"}, long)
	env.waitTask(t, env.submit(t, env.groupEvent("20001", "说说", mentionBot)).TaskID)
	got, _ := toolMessages(env.short.Requests()[1])[0]["content"].(string)
	if n := utf8.RuneCountInString(got); n != 2000 {
		t.Errorf("tool result = %d runes, want 2000", n)
	}
	if long.args[0] != "{}" {
		t.Errorf("empty arguments passed as %q, want {}", long.args[0])
	}
}

func TestSkills_InvocationIsTheSpeaker(t *testing.T) {
	now := &recTool{name: "now"}
	env := skillEnv(t, testOpts{shortTools: callOnceThen("好", fakeopenai.ToolCall{ID: "a", Name: "now", Arguments: "{}"})},
		[]string{"time"}, now)
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点", withName("小明"), mentionBot)).TaskID)
	inv := now.invs[0]
	if inv.TaskID != task.TaskID || inv.BotInstanceID != testBot || inv.ConversationKey != testConvKey ||
		inv.ConversationType != conloomapi.ConvGroup || inv.PlatformUserID != "20001" || inv.DisplayName != "小明" ||
		inv.IsAdmin || inv.Location != env.o.loc || inv.Now.IsZero() {
		t.Errorf("group invocation = %+v", inv)
	}

	env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", "几点")).TaskID)
	inv = now.invs[1]
	if inv.ConversationType != conloomapi.ConvPrivate || inv.PlatformUserID != "29999" || !inv.IsAdmin {
		t.Errorf("private invocation = %+v", inv)
	}
}

func TestSkills_RerouteDoesNotRepeatCalls(t *testing.T) {
	now := &recTool{name: "now"}
	env := skillEnv(t, testOpts{
		shortTools: func(req map[string]any) fakeopenai.Reply {
			if hasToolResult(req) {
				return fakeopenai.Reply{Status: 500, Content: "boom"}
			}
			return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "s", Name: "now", Arguments: "{}"}}}
		},
		longTools: callOnceThen("长模型说现在四点半", fakeopenai.ToolCall{ID: "l", Name: "now", Arguments: "{}"}),
	}, []string{"time"}, now)
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != "长模型说现在四点半" || task.Meta.SelectedModelRole != "long_context" {
		t.Fatalf("task = %+v", task)
	}
	if now.count() != 1 {
		t.Errorf("now executed %d times, want 1 (the re-routed loop reuses the result)", now.count())
	}
	if got := toolMessages(env.long.Requests()[1]); len(got) != 1 || got[0]["content"] != nowResult {
		t.Errorf("long model tool results = %v", got)
	}
}
