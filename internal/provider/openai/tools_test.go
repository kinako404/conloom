package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
)

func TestChatMessage_JSON(t *testing.T) {
	cases := []struct {
		name string
		msg  ChatMessage
		want string
	}{
		{"plain", ChatMessage{Role: "user", Content: "你好"}, `{"role":"user","content":"你好"}`},
		{"plain empty content", ChatMessage{Role: "user"}, `{"role":"user","content":""}`},
		{"tool calls without content", ChatMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "now", Arguments: "{}"}}},
			`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"now","arguments":"{}"}}]}`},
		{"tool calls with content", ChatMessage{Role: "assistant", Content: "我查一下",
			ToolCalls: []ToolCall{{ID: "call_1", Name: "recall", Arguments: `{"query":"草莓"}`}, {ID: "call_2", Name: "now", Arguments: ""}}},
			`{"role":"assistant","content":"我查一下","tool_calls":[{"id":"call_1","type":"function","function":{"name":"recall","arguments":"{\"query\":\"草莓\"}"}},` +
				`{"id":"call_2","type":"function","function":{"name":"now","arguments":""}}]}`},
		{"tool result", ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "调用失败。"},
			`{"role":"tool","content":"调用失败。","tool_call_id":"call_1"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.msg)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.want {
				t.Fatalf("Marshal = %s\nwant      %s", b, c.want)
			}
			var back ChatMessage
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, c.msg) {
				t.Errorf("round trip = %+v, want %+v", back, c.msg)
			}
		})
	}
}

// captureServer records the request body and answers with message.
func captureServer(t *testing.T, message string) (*httptest.Server, <-chan map[string]any) {
	t.Helper()
	got := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		got <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m1","choices":[{"message":`+message+`,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestChat_SendsToolsAndToolChoice(t *testing.T) {
	srv, got := captureServer(t, `{"role":"assistant","content":"好"}`)
	c := newTestClient(testProfile(srv.URL))
	req := userRequest()
	req.Tools = []Tool{
		{Name: "recall", Description: "查聊天记录", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)},
		{Name: "now", Description: "当前时间"},
	}
	req.ToolChoice = "none"
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	body := <-got
	b, _ := json.Marshal(body["tools"])
	want := `[{"function":{"description":"查聊天记录","name":"recall","parameters":{"properties":{"query":{"type":"string"}},"required":["query"],"type":"object"}},"type":"function"},` +
		`{"function":{"description":"当前时间","name":"now","parameters":{"properties":{},"type":"object"}},"type":"function"}]`
	if string(b) != want {
		t.Errorf("tools = %s\nwant    %s", b, want)
	}
	if body["tool_choice"] != "none" {
		t.Errorf("tool_choice = %v", body["tool_choice"])
	}

	if _, err := c.Chat(context.Background(), userRequest()); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	body = <-got
	for _, key := range []string{"tools", "tool_choice"} {
		if _, ok := body[key]; ok {
			t.Errorf("request without tools has %q: %v", key, body)
		}
	}
}

func TestChat_ToolsKeepPlainRequestBytes(t *testing.T) {
	c := newTestClient(testProfile("http://127.0.0.1:9"))
	got, err := c.requestBody(ChatRequest{Messages: []ChatMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "你好"}}, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"max_tokens":100,"messages":[{"role":"system","content":"s"},{"role":"user","content":"你好"}],"model":"m1","stream":false}`
	if string(got) != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
}

func TestChat_ParsesToolCalls(t *testing.T) {
	cases := []struct {
		name, message string
		want          ChatResponse
	}{
		{"null content", `{"role":"assistant","content":null,"tool_calls":[` +
			`{"id":"call_a","type":"function","function":{"name":"remember","arguments":"{\"content\":\"喜欢草莓\"}"}},` +
			`{"id":"call_b","type":"function","function":{"name":"now","arguments":"{}"}}]}`,
			ChatResponse{ToolCalls: []ToolCall{{ID: "call_a", Name: "remember", Arguments: `{"content":"喜欢草莓"}`}, {ID: "call_b", Name: "now", Arguments: "{}"}}}},
		{"content is only reasoning", `{"role":"assistant","content":"<think>要记下来</think>","tool_calls":[` +
			`{"id":"call_a","type":"function","function":{"name":"now","arguments":""}}]}`,
			ChatResponse{ToolCalls: []ToolCall{{ID: "call_a", Name: "now", Arguments: ""}}}},
		{"content kept", `{"role":"assistant","content":"稍等","tool_calls":[{"id":"x","type":"function","function":{"name":"now","arguments":"{}"}}]}`,
			ChatResponse{Content: "稍等", ToolCalls: []ToolCall{{ID: "x", Name: "now", Arguments: "{}"}}}},
		{"object arguments and missing id", `{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"recall","arguments":{"query":"草莓"}}}]}`,
			ChatResponse{ToolCalls: []ToolCall{{ID: "", Name: "recall", Arguments: `{"query":"草莓"}`}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := captureServer(t, c.message)
			resp, err := newTestClient(testProfile(srv.URL)).Chat(context.Background(), userRequest())
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if resp.Content != c.want.Content || !reflect.DeepEqual(resp.ToolCalls, c.want.ToolCalls) || resp.FinishReason != "tool_calls" {
				t.Errorf("resp = %+v, want content %q tool calls %+v", resp, c.want.Content, c.want.ToolCalls)
			}
		})
	}

	srv, _ := captureServer(t, `{"role":"assistant","content":null}`)
	if _, err := newTestClient(testProfile(srv.URL)).Chat(context.Background(), userRequest()); !errors.Is(err, ErrEmptyOutput) {
		t.Errorf("null content without tool calls: err = %v, want ErrEmptyOutput", err)
	}
}

func TestChat_FakeServerToolCalls(t *testing.T) {
	fake := fakeopenai.NewWithTools(t, func(map[string]any) fakeopenai.Reply {
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "call_1", Name: "now", Arguments: "{}"}}}
	})
	resp, err := newTestClient(testProfile(fake.URL)).Chat(context.Background(), userRequest())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if want := []ToolCall{{ID: "call_1", Name: "now", Arguments: "{}"}}; resp.Content != "" || !reflect.DeepEqual(resp.ToolCalls, want) {
		t.Errorf("resp = %+v", resp)
	}
}
