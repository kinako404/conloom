package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
)

// noteTool stands in for a built-in memory tool: remember keeps what it is
// told, the others answer a fixed text.
type noteTool struct {
	name string

	mu    sync.Mutex
	notes []string
	users []string
}

func (n *noteTool) Spec() skills.Spec {
	return skills.Spec{Name: n.name, Description: "fake " + n.name,
		Parameters: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string"}}}`)}
}

func (n *noteTool) Call(_ context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	var a struct{ Content string }
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notes = append(n.notes, a.Content)
	n.users = append(n.users, inv.PlatformUserID+"@"+inv.ConversationKey)
	return "已记住。", nil
}

func TestE2E_SkillRememberRoundTrip(t *testing.T) {
	remember := &noteTool{name: "remember"}
	var mu sync.Mutex
	var requests []map[string]any
	fake := fakeopenai.NewWithTools(t, func(req map[string]any) fakeopenai.Reply {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		if strings.Contains(requestToolResults(req), "已记住。") {
			return fakeopenai.Reply{Content: "好，记下了，你喜欢草莓。"}
		}
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "call_1", Name: "remember", Arguments: `{"content":"喜欢草莓"}`}}}
	})
	cfg := testConfig(t, configParams{modelURL: fake.URL})
	cfg.Bots[0].Skills = []string{"memory"}
	e := newEnvWith(t, cfg, app.Options{Tools: []skills.Tool{remember, &noteTool{name: "recall"}, &noteTool{name: "get_affinity"}}})

	e.mention(group, alice, "记住我喜欢草莓")
	a := e.drainOne()
	if a.Content.Text != "好，记下了，你喜欢草莓。" {
		t.Fatalf("reply = %q", a.Content.Text)
	}
	for _, trace := range []string{"remember", "工具", "{", "call_1", "已记住"} {
		if strings.Contains(a.Content.Text, trace) {
			t.Errorf("reply %q shows %q", a.Content.Text, trace)
		}
	}
	remember.mu.Lock()
	notes, users := remember.notes, remember.users
	remember.mu.Unlock()
	if len(notes) != 1 || notes[0] != "喜欢草莓" || users[0] != "20001@bot_main:qq:group:group_10001" {
		t.Errorf("remembered %v for %v", notes, users)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(requests))
	}
	if !strings.Contains(requestText(requests[0]), "【技能】") {
		t.Error("first request lacks the skill section")
	}
}

// requestToolResults joins the contents of the tool messages of req.
func requestToolResults(req map[string]any) string {
	var sb strings.Builder
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "tool" {
			c, _ := mm["content"].(string)
			sb.WriteString(c)
		}
	}
	return sb.String()
}
