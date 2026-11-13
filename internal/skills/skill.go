// Package skills lets the model call built-in tools, grouped into skills that a
// bot enables in its configuration.
package skills

import (
	"context"
	"encoding/json"
	"time"
)

// Spec describes a tool to the model: its name, when to use it and the JSON
// Schema of its arguments.
type Spec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Invocation is who a tool call acts for: always the speaker of the message
// being answered, in that message's conversation. Tools never take a user or
// conversation from the model's arguments.
type Invocation struct {
	TaskID           string
	BotInstanceID    string
	ConversationKey  string
	ConversationType string // conloomapi.ConvGroup or conloomapi.ConvPrivate
	PlatformUserID   string
	DisplayName      string
	// TriggerMessageIDs 是本次回复所回应的消息（任务的触发消息），按时间顺序。
	TriggerMessageIDs []string
	IsAdmin           bool
	Now               time.Time
	Location          *time.Location
}

// Tool is a built-in function that skills expose to the model.
type Tool interface {
	Spec() Spec
	// Call runs the tool with the model's JSON arguments and returns the text
	// handed back to the model. An error is reported to the model as a failed
	// call and never reaches the user.
	Call(ctx context.Context, inv Invocation, args json.RawMessage) (string, error)
}
