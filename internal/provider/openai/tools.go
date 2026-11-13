package openai

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Tool is a function the model may call. Nil Parameters is a function without
// arguments.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall is one function call requested by the model. Arguments is the JSON
// text of the arguments as the model wrote it.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

var noParameters = json.RawMessage(`{"type":"object","properties":{}}`)

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

func (t Tool) MarshalJSON() ([]byte, error) {
	params := t.Parameters
	if len(bytes.TrimSpace(params)) == 0 {
		params = noParameters
	}
	return json.Marshal(struct {
		Type     string       `json:"type"`
		Function wireFunction `json:"function"`
	}{"function", wireFunction{t.Name, t.Description, params}})
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func (c ToolCall) MarshalJSON() ([]byte, error) {
	var w wireToolCall
	w.ID, w.Type, w.Function.Name = c.ID, "function", c.Name
	args, err := json.Marshal(c.Arguments)
	if err != nil {
		return nil, err
	}
	w.Function.Arguments = args
	return json.Marshal(w)
}

// UnmarshalJSON accepts arguments as the standard JSON string or, as some
// servers send them, as a JSON value.
func (c *ToolCall) UnmarshalJSON(b []byte) error {
	var w wireToolCall
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	c.ID, c.Name = w.ID, w.Function.Name
	args := bytes.TrimSpace(w.Function.Arguments)
	switch {
	case len(args) == 0 || bytes.Equal(args, []byte("null")):
		c.Arguments = ""
	case args[0] == '"':
		return json.Unmarshal(args, &c.Arguments)
	default:
		c.Arguments = string(args)
	}
	return nil
}

// wireMessage is ChatMessage on the wire: content is null for an assistant
// message that only calls tools, as OpenAI-compatible servers send it.
type wireMessage struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

func (m ChatMessage) MarshalJSON() ([]byte, error) {
	w := wireMessage{Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	if m.Content != "" || len(m.ToolCalls) == 0 {
		w.Content = &m.Content
	}
	return json.Marshal(w)
}

func (m *ChatMessage) UnmarshalJSON(b []byte) error {
	var w wireMessage
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*m = ChatMessage{Role: w.Role, ToolCalls: w.ToolCalls, ToolCallID: w.ToolCallID}
	if w.Content != nil {
		m.Content = *w.Content
	}
	return nil
}

// withIDs gives every call without an ID a synthetic one, so the tool results
// sent back can always refer to their call.
func withIDs(calls []ToolCall) []ToolCall {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = "call_" + strconv.Itoa(i)
		}
	}
	return calls
}
