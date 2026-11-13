package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
)

// limitedTool is a recTool that declares the longest result it returns.
type limitedTool struct {
	*recTool
	limit int
}

func (l limitedTool) MaxResultRunes() int { return l.limit }

// exampleShortModel is the short model of conloom.example.yaml: a 32768
// token window, a 16000 token soft budget and 1024 tokens each for the output
// and the safety margin.
func exampleShortModel(c *config.Config) {
	p := c.ModelProfiles["short_context"]
	p.Context.NativeWindow, p.Context.ConfiguredWindow, p.Context.SoftInputBudget = 32768, 32768, 16000
	p.Context.ReservedOutputTokens, p.Context.SafetyMarginTokens = 1024, 1024
	c.ModelProfiles["short_context"] = p
	c.DefaultGroupPolicy.Concurrency.AllowLongRequests = false
	c.DefaultGroupPolicy.Context.MaxRecentMessages = 400
}

// historyShown counts the history messages "史NNN" in the first request.
func historyShown(t *testing.T, env *testEnv) int {
	t.Helper()
	text := requestText(t, env.short.Requests()[0])
	n := 0
	for i := range 300 {
		if strings.Contains(text, fmt.Sprintf("史%03d", i)) {
			n++
		}
	}
	return n
}

func TestSkills_ToolRoundDoesNotShrinkTheSoftBudget(t *testing.T) {
	text := func(map[string]any) fakeopenai.Reply { return fakeopenai.Reply{Content: "好"} }
	shown := map[bool]int{}
	for _, enabled := range []bool{false, true} {
		var botSkills []string
		if enabled {
			botSkills = []string{"time"}
		}
		env := skillEnv(t, testOpts{shortTools: text, configure: exampleShortModel}, botSkills, &recTool{name: "now"})
		for i := range 300 {
			env.submit(t, env.groupEvent("20002", fmt.Sprintf("史%03d", i)+strings.Repeat("闲", 100)))
		}
		env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
		if got := len(offeredTools(env.short.Requests()[0])); (got > 0) != enabled {
			t.Fatalf("enabled=%v: tools offered %d", enabled, got)
		}
		shown[enabled] = historyShown(t, env)
	}
	// The example model has 30720 prompt tokens: a 16000 token budget plus a
	// worst-case round (about 9000 tokens) fit, so skills cost the history
	// only the room of the skill section and tool definitions (a few
	// messages), not a tool round (about 85 messages of 105 tokens).
	if shown[false] < 120 || shown[false]-shown[true] > 12 {
		t.Errorf("history shown: %d without skills, %d with; want at most 12 fewer", shown[false], shown[true])
	}
}

func TestSkills_DeclaredResultLimits(t *testing.T) {
	long := &recTool{name: "now", fn: func(json.RawMessage) (string, error) { return strings.Repeat("长", 50), nil }}
	env := skillEnv(t, testOpts{shortTools: callOnceThen("好", fakeopenai.ToolCall{ID: "a", Name: "now", Arguments: "{}"})},
		[]string{"time"}, limitedTool{long, 10})
	env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
	got, _ := toolMessages(env.short.Requests()[1])[0]["content"].(string)
	if body := strings.TrimPrefix(got, toolResultPreface+"\n"); utf8.RuneCountInString(body) != 10 {
		t.Errorf("result = %q, want the declared 10 runes", got)
	}
}

func TestSkills_WorstRoundFollowsDeclaredLimits(t *testing.T) {
	small := worstRound(256, 1, 100)
	recall := worstRound(256, 1, 1500)
	undeclared := worstRound(256, 1, maxToolResultRunes)
	if !(small < recall && recall < undeclared) {
		t.Errorf("worst rounds = %d, %d, %d; want them to grow with the result limit", small, recall, undeclared)
	}
}

func TestSkills_ToolCallWrittenAsTextIsNotSent(t *testing.T) {
	for _, content := range []string{
		`<tool_call>{"name":"now","arguments":{}}</tool_call>`,
		"我查一下\n<function=now>{}</function>",
		"<TOOL_CALL>now</TOOL_CALL>",
	} {
		t.Run(content, func(t *testing.T) {
			env := skillEnv(t, testOpts{
				shortTools: func(map[string]any) fakeopenai.Reply { return fakeopenai.Reply{Content: content} },
				longTools:  func(map[string]any) fakeopenai.Reply { return fakeopenai.Reply{Content: "好的"} },
			}, []string{"time"}, &recTool{name: "now"})
			task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
			if len(task.Actions) != 1 || task.Actions[0].Content.Text != "好的" || task.Meta.SelectedModelRole != "long_context" {
				t.Fatalf("task = %+v, want the re-routed answer", task)
			}
			if !strings.Contains(env.logs.String(), "tool call written as text") {
				t.Errorf("logs lack the reason")
			}
		})
	}
}

func TestSkills_TruncatedToolRoundIsNotSent(t *testing.T) {
	env := skillEnv(t, testOpts{
		shortTools: func(map[string]any) fakeopenai.Reply {
			return fakeopenai.Reply{Content: "我来查一下时间，调用 n", FinishReason: "length"}
		},
		longTools: func(map[string]any) fakeopenai.Reply { return fakeopenai.Reply{Content: "四点半"} },
	}, []string{"time"}, &recTool{name: "now"})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "几点了", mentionBot)).TaskID)
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != "四点半" {
		t.Fatalf("task = %+v, want the re-routed answer", task)
	}
	if !strings.Contains(env.logs.String(), "truncated tool round") {
		t.Errorf("logs lack the reason")
	}

	// A truncated final answer is still an answer.
	final := skillEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
		if len(offeredTools(req)) == 0 {
			return fakeopenai.Reply{Content: "四点", FinishReason: "length"}
		}
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: fmt.Sprintf("c%d", len(toolMessages(req))), Name: "now",
			Arguments: fmt.Sprintf(`{"n":%d}`, len(toolMessages(req)))}}}
	}}, []string{"time"}, &recTool{name: "now"})
	task = final.waitTask(t, final.submit(t, final.groupEvent("20001", "几点了", mentionBot)).TaskID)
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != "四点" {
		t.Errorf("truncated final answer: task = %+v", task)
	}
}
