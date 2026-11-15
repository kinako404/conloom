package openai

import (
	"bytes"
	"encoding/json"
)

// Tool is a function the model may call. Nil Parameters is a function without
// arguments.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall is one function call requested by the model. Arguments is the JSON
// text of the arguments as the model wrote it; ID is empty when the server
// sent none.
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

// wireMessage is a ChatMessage without images on the wire: content is null
// for an assistant message that only calls tools, as OpenAI-compatible
// servers send it.
type wireMessage struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// wirePartsMessage is a ChatMessage with images on the wire: its content is
// the image parts followed by a text part, left out when the text is empty.
type wirePartsMessage struct {
	Role       string     `json:"role"`
	Content    []wirePart `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type wirePart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL string `json:"url"`
}

func (m ChatMessage) MarshalJSON() ([]byte, error) {
	if len(m.Images) > 0 {
		w := wirePartsMessage{Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
		for _, url := range m.Images {
			w.Content = append(w.Content, wirePart{Type: "image_url", ImageURL: &wireImageURL{URL: url}})
		}
		if m.Content != "" {
			w.Content = append(w.Content, wirePart{Type: "text", Text: m.Content})
		}
		return json.Marshal(w)
	}
	w := wireMessage{Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	if m.Content != "" || len(m.ToolCalls) == 0 {
		w.Content = &m.Content
	}
	return json.Marshal(w)
}

// UnmarshalJSON accepts content as a string, null, or an array of parts whose
// images become Images and whose texts, concatenated, become Content.
func (m *ChatMessage) UnmarshalJSON(b []byte) error {
	var w struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*m = ChatMessage{Role: w.Role, ToolCalls: w.ToolCalls, ToolCallID: w.ToolCallID}
	content := bytes.TrimSpace(w.Content)
	switch {
	case len(content) == 0 || bytes.Equal(content, []byte("null")):
		return nil
	case content[0] != '[':
		return json.Unmarshal(content, &m.Content)
	}
	var parts []wirePart
	if err := json.Unmarshal(content, &parts); err != nil {
		return err
	}
	for _, p := range parts {
		switch {
		case p.Type == "image_url" && p.ImageURL != nil:
			m.Images = append(m.Images, p.ImageURL.URL)
		case p.Type == "text":
			m.Content += p.Text
		}
	}
	return nil
}
