package core

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// stateTool is a recTool that changes state.
type stateTool struct{ *recTool }

func (stateTool) ChangesState() bool { return true }

const mixedBody = "- 用 now 查时间，用 mark 记下变化。"

// stepEnv is a test orchestrator in state step mode mode whose bot enables the
// skill mixed (private and group, tools now and mark, a state tool); override
// replaces skills (a mixed among them replaces it). Everyone may chat
// privately and both profiles turn thinking on through chat_template_kwargs.
func stepEnv(t *testing.T, opts testOpts, mode string, override ...skills.Skill) (env *testEnv, now *recTool, mark stateTool) {
	t.Helper()
	now, mark = &recTool{name: "now"}, stateTool{&recTool{name: "mark"}}
	return stepEnvOf(t, opts, mode, now, mark, override...), now, mark
}

// stepEnvOf is stepEnv with the tools now and mark given.
func stepEnvOf(t *testing.T, opts testOpts, mode string, now, mark skills.Tool, override ...skills.Skill) *testEnv {
	t.Helper()
	mixed := skills.Skill{Name: "mixed", Description: "d", Tools: []string{"now", "mark"},
		Conversations: []string{"private", "group"}, Body: mixedBody}
	configure := opts.configure
	opts.configure = func(c *config.Config) {
		c.Bots[0].PrivateChat = "all"
		for _, role := range []string{"short_context", "long_context"} {
			p := c.ModelProfiles[role]
			p.ExtraBody = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true}}
			c.ModelProfiles[role] = p
		}
		c.Skills.StateStep = mode
		if configure != nil {
			configure(c)
		}
	}
	return skillEnvWith(t, opts, append([]skills.Skill{mixed}, override...), []string{"mixed"}, now, mark)
}

// thinking returns chat_template_kwargs.enable_thinking of req, nil when unset.
func thinking(req map[string]any) any {
	kw, _ := req["chat_template_kwargs"].(map[string]any)
	return kw["enable_thinking"]
}

// replyRequests drops the state step requests (see isStateRequest) from reqs.
func replyRequests(t *testing.T, reqs []map[string]any) []map[string]any {
	t.Helper()
	return slices.DeleteFunc(slices.Clone(reqs), func(req map[string]any) bool { return isStateRequest(t, req) })
}

func systemText(t *testing.T, req map[string]any) string {
	t.Helper()
	sys, _ := requestMessages(t, req)[0]["content"].(string)
	return sys
}

func TestReplyStep_OffersOnlyLookupsWithThinkingOff(t *testing.T) {
	env, now, mark := stepEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
		switch {
		case len(replyRequests(t, []map[string]any{req})) == 0:
			return fakeopenai.Reply{Content: "好"}
		case hasToolResult(req):
			return fakeopenai.Reply{Content: "现在四点半。"}
		}
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "c1", Name: "now", Arguments: "{}"}}}
	}}, config.StateStepAfterReply)
	task := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "几点了")).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != "现在四点半。" {
		t.Fatalf("task = %+v", task)
	}
	reqs := replyRequests(t, env.short.Requests())
	if len(reqs) != 2 {
		t.Fatalf("reply requests = %d, want 2", len(reqs))
	}
	for i, req := range reqs {
		if got := offeredTools(req); !reflect.DeepEqual(got, []string{"now"}) {
			t.Errorf("request %d tools = %v, want [now]", i, got)
		}
		if got := thinking(req); got != false {
			t.Errorf("request %d enable_thinking = %v, want false", i, got)
		}
		if sys := systemText(t, req); !strings.Contains(sys, mixedBody+"\n"+copyReplyStepNote) {
			t.Errorf("request %d system prompt lacks the section and the note: %q", i, sys)
		}
	}
	if now.count() != 1 {
		t.Errorf("now called %d times, want 1", now.count())
	}
	if mark.count() != 0 {
		t.Errorf("mark called %d times by the reply", mark.count())
	}
}

func TestReplyStep_NoLookupsIsOneRequest(t *testing.T) {
	onlyMark := skills.Skill{Name: "mixed", Description: "d", Tools: []string{"mark"},
		Conversations: []string{"private", "group"}, Body: mixedBody}
	env, _, mark := stepEnv(t, testOpts{
		shortTools: func(req map[string]any) fakeopenai.Reply {
			if len(replyRequests(t, []map[string]any{req})) == 0 {
				return fakeopenai.Reply{Content: "好"}
			}
			return fakeopenai.Reply{Content: `<tool_call>{"name":"mark","arguments":{}}</tool_call>`}
		},
		longTools: func(req map[string]any) fakeopenai.Reply {
			if len(replyRequests(t, []map[string]any{req})) == 0 {
				return fakeopenai.Reply{Content: "好"}
			}
			return fakeopenai.Reply{Content: fakeReply}
		},
	}, config.StateStepAfterReply, onlyMark)
	task := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "我先去洗澡了")).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply ||
		task.Meta.SelectedModelRole != "long_context" {
		t.Fatalf("task = %+v, want the re-routed reply", task)
	}
	short, long := replyRequests(t, env.short.Requests()), replyRequests(t, env.long.Requests())
	if len(short) != 1 || len(long) != 1 {
		t.Fatalf("reply requests = %d short, %d long; want one each", len(short), len(long))
	}
	for _, req := range []map[string]any{short[0], long[0]} {
		if _, ok := req["tools"]; ok {
			t.Errorf("request offers tools: %v", req["tools"])
		}
		if got := thinking(req); got != false {
			t.Errorf("enable_thinking = %v, want false", got)
		}
		if sys := systemText(t, req); !strings.Contains(sys, mixedBody+"\n"+copyReplyStepNote) {
			t.Errorf("system prompt lacks the section and the note: %q", sys)
		}
		if strings.Contains(requestText(t, req), copyToolsGone) {
			t.Errorf("request carries copyToolsGone")
		}
	}
	if !strings.Contains(env.logs.String(), "tool call written as text") {
		t.Errorf("logs lack the reason")
	}
	if mark.count() != 0 {
		t.Errorf("mark called %d times by the reply", mark.count())
	}
}

func TestReplyStep_NoLookupsToolCallsAreNotAnAnswer(t *testing.T) {
	onlyMark := skills.Skill{Name: "mixed", Description: "d", Tools: []string{"mark"},
		Conversations: []string{"private", "group"}, Body: mixedBody}
	env, _, mark := stepEnv(t, testOpts{
		shortTools: func(req map[string]any) fakeopenai.Reply {
			if isStateRequest(t, req) {
				return fakeopenai.Reply{Content: "好"}
			}
			return fakeopenai.Reply{Content: "稍等，我记一下。", ToolCalls: []fakeopenai.ToolCall{{ID: "c1", Name: "mark", Arguments: "{}"}}}
		},
		longTools: func(req map[string]any) fakeopenai.Reply {
			if isStateRequest(t, req) {
				return fakeopenai.Reply{Content: "好"}
			}
			return fakeopenai.Reply{Content: fakeReply}
		},
	}, config.StateStepAfterReply, onlyMark)
	task := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "我先去洗澡了")).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply ||
		task.Meta.SelectedModelRole != "long_context" {
		t.Fatalf("task = %+v, want the re-routed reply", task)
	}
	if !strings.Contains(env.logs.String(), reasonToolCallsUnoffered) {
		t.Errorf("logs lack the reason")
	}
	waitStateIdle(t, env)
	if mark.count() != 0 {
		t.Errorf("mark called %d times", mark.count())
	}
}

func TestReplyStep_NoLookupsTruncatedReplyIsSent(t *testing.T) {
	env, _, _ := stepEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
		if len(replyRequests(t, []map[string]any{req})) == 0 {
			return fakeopenai.Reply{Content: "好"}
		}
		return fakeopenai.Reply{Content: "说到一半", FinishReason: "length"}
	}}, config.StateStepAfterReply, skills.Skill{Name: "mixed", Description: "d", Tools: []string{"mark"},
		Conversations: []string{"private", "group"}, Body: mixedBody})
	task := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "你在干嘛")).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != "说到一半" ||
		task.Meta.SelectedModelRole != "short_context" {
		t.Fatalf("task = %+v, want the cut reply sent", task)
	}
}

func TestReplyStep_UnsplitTurnsUnchanged(t *testing.T) {
	at := time.Now()
	for _, c := range []struct {
		name   string
		skills []string
		tools  []string
	}{
		{"no skills", nil, nil},
		{"only lookups", []string{"privnote"}, []string{"now"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var reqs [][]map[string]any
			for _, mode := range []string{config.StateStepAfterReply, config.StateStepInline} {
				env, now, _ := stepEnv(t, testOpts{
					shortTools: func(req map[string]any) fakeopenai.Reply {
						if len(offeredTools(req)) == 0 || hasToolResult(req) {
							return fakeopenai.Reply{Content: "现在四点半。"}
						}
						return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "c1", Name: "now", Arguments: "{}"}}}
					},
					configure: func(cfg *config.Config) { cfg.Bots[0].Skills = c.skills },
				}, mode)
				task := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "几点了", occurredAt(at))).TaskID)
				if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
					t.Fatalf("%s: task = %+v", mode, task)
				}
				got := env.short.Requests()
				for i, req := range got {
					if tools := offeredTools(req); !reflect.DeepEqual(tools, c.tools) {
						t.Errorf("%s: request %d tools = %v, want %v", mode, i, tools, c.tools)
					}
					if thinking(req) != true {
						t.Errorf("%s: request %d enable_thinking = %v, want true", mode, i, thinking(req))
					}
					if strings.Contains(requestText(t, req), copyReplyStepNote) {
						t.Errorf("%s: request %d carries the reply step note", mode, i)
					}
				}
				if want := len(c.tools) + 1; len(got) != want || now.count() != len(c.tools) {
					t.Errorf("%s: requests = %d, now calls = %d; want %d and %d", mode, len(got), now.count(), want, len(c.tools))
				}
				reqs = append(reqs, got)
			}
			if !reflect.DeepEqual(reqs[0], reqs[1]) {
				t.Errorf("after_reply requests differ from inline:\n%v\n%v", reqs[0], reqs[1])
			}
		})
	}
}

func TestReplyStep_InlineModeUnchanged(t *testing.T) {
	at := time.Now()
	model := func(req map[string]any) fakeopenai.Reply {
		if len(offeredTools(req)) == 0 {
			return fakeopenai.Reply{Content: "记下了。"}
		}
		n := len(toolMessages(req))
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{
			{ID: fmt.Sprintf("n%d", n), Name: "now", Arguments: "{}"},
			{ID: fmt.Sprintf("m%d", n), Name: "mark", Arguments: fmt.Sprintf(`{"n":%d}`, n)}}}
	}
	var reqs [][]map[string]any
	for _, c := range []struct {
		mode  string
		state bool
	}{{config.StateStepInline, true}, {config.StateStepAfterReply, false}} {
		now, rec := &recTool{name: "now"}, &recTool{name: "mark"}
		var mark skills.Tool = rec
		if c.state {
			mark = stateTool{rec}
		}
		env := stepEnvOf(t, testOpts{shortTools: model}, c.mode, now, mark)
		task := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "我先去洗澡了", occurredAt(at))).TaskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != "记下了。" {
			t.Fatalf("%s: task = %+v", c.mode, task)
		}
		got := env.short.Requests()
		if len(got) != maxToolRounds+1 {
			t.Fatalf("%s: requests = %d, want %d rounds and a final request", c.mode, len(got), maxToolRounds)
		}
		for i, req := range got {
			final := i == maxToolRounds
			if tools := offeredTools(req); final != (tools == nil) || !final && !reflect.DeepEqual(tools, []string{"now", "mark"}) {
				t.Errorf("%s: request %d tools = %v", c.mode, i, tools)
			}
			if thinking(req) != !final {
				t.Errorf("%s: request %d enable_thinking = %v, want %v", c.mode, i, thinking(req), !final)
			}
			if strings.Contains(requestText(t, req), copyReplyStepNote) {
				t.Errorf("%s: request %d carries the reply step note", c.mode, i)
			}
		}
		if rec.count() != maxToolRounds {
			t.Errorf("%s: mark called %d times in the reply loop, want %d", c.mode, rec.count(), maxToolRounds)
		}
		reqs = append(reqs, got)
	}
	if !reflect.DeepEqual(reqs[0], reqs[1]) {
		t.Errorf("inline requests with a state tool differ from those with only lookups:\n%v\n%v", reqs[0], reqs[1])
	}
}
