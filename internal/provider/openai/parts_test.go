package openai

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestChatMessage_PartsInOrder(t *testing.T) {
	m := ChatMessage{Role: "user", Content: "ignored", Images: []string{"data:z"},
		Parts: []ChatPart{{Text: "A"}, {ImageURL: "data:x"}, {Text: ""}, {Text: "B"}}}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"A"},{"type":"image_url","image_url":{"url":"data:x"}},{"type":"text","text":"B"}]}`
	if string(got) != want {
		t.Fatalf("Marshal = %s\nwant      %s", got, want)
	}
	var back ChatMessage
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	wantBack := ChatMessage{Role: "user", Content: "AB", Images: []string{"data:x"}}
	if !reflect.DeepEqual(back, wantBack) {
		t.Errorf("Unmarshal = %+v, want %+v", back, wantBack)
	}
}

func TestChatMessage_WithoutPartsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		msg  ChatMessage
		want string
	}{
		{"user text", ChatMessage{Role: "user", Content: "你好"}, `{"role":"user","content":"你好"}`},
		{"tool calls only", ChatMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "now", Arguments: "{}"}}},
			`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"now","arguments":"{}"}}]}`},
		{"tool result", ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "调用失败。"},
			`{"role":"tool","content":"调用失败。","tool_call_id":"call_1"}`},
		{"images", ChatMessage{Role: "user", Content: "你好", Images: []string{"data:a", "data:b"}},
			`{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:a"}},{"type":"image_url","image_url":{"url":"data:b"}},{"type":"text","text":"你好"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, parts := range [][]ChatPart{nil, {}} {
				m := c.msg
				m.Parts = parts
				got, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != c.want {
					t.Fatalf("Parts %#v: Marshal = %s\nwant      %s", parts, got, c.want)
				}
			}
		})
	}
}
