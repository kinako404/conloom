// Package conloomapi defines the public wire types of the ConLoom Bot Interaction API.
package conloomapi

import "time"

const SchemaVersion = "1.0"

const EventTypeMessageCreated = "message.created"

const (
	ConvGroup   = "group"
	ConvPrivate = "private"
)

const (
	DispositionReply   = "reply"
	DispositionObserve = "observe"
	DispositionIgnore  = "ignore"
)

const (
	SuppressedRateLimited       = "rate_limited"
	SuppressedQueueFull         = "queue_full"
	SuppressedPaused            = "paused"
	SuppressedPrivateNotAllowed = "private_not_allowed"
	SuppressedNotEnabled        = "not_enabled"
	SuppressedSelfMessage       = "self_message"
)

const (
	TaskQueued    = "queued"
	TaskRunning   = "running"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

const ActionMessageSend = "message.send"

const (
	AckSent        = "sent"
	AckUnsupported = "unsupported"
	AckFailed      = "failed"
)

const (
	ScopeEventsWrite = "events:write"
	ScopeTasksRead   = "tasks:read"
	ScopeOutboxRead  = "outbox:read"
	ScopeOutboxAck   = "outbox:ack"
	ScopeMemoryRead  = "memory:read"
	ScopeMemoryWrite = "memory:write"
	ScopePersonaRead = "persona:read"
	ScopePolicyWrite = "policy:write"
	ScopeAdminRead   = "admin:read"
)

const (
	TriggerPrivate = "private"
	TriggerMention = "mention"
	TriggerReply   = "reply"
	TriggerCommand = "command"
	TriggerObserve = "observe"
	TriggerManual  = "manual"
)

type Event struct {
	SchemaVersion string         `json:"schema_version"`
	EventID       string         `json:"event_id"`
	EventType     string         `json:"event_type"`
	BotInstanceID string         `json:"bot_instance_id"`
	Platform      string         `json:"platform"`
	OccurredAt    time.Time      `json:"occurred_at"`
	Conversation  Conversation   `json:"conversation"`
	Actor         Actor          `json:"actor"`
	Trigger       Trigger        `json:"trigger"`
	Message       Message        `json:"message"`
	Extensions    map[string]any `json:"extensions"`
}

type Conversation struct {
	Type                   string `json:"type"`
	ID                     string `json:"id"`
	PlatformConversationID string `json:"platform_conversation_id"`
}

type Actor struct {
	ID             string   `json:"id"`
	PlatformUserID string   `json:"platform_user_id"`
	DisplayName    string   `json:"display_name"`
	Roles          []string `json:"roles"`
}

type Trigger struct {
	Type              string `json:"type"`
	RequestedResponse bool   `json:"requested_response"`
}

type Message struct {
	ID                string       `json:"id"`
	PlatformMessageID string       `json:"platform_message_id,omitempty"`
	Text              string       `json:"text"`
	ReplyTo           *string      `json:"reply_to"`
	Mentions          []Mention    `json:"mentions"`
	Attachments       []Attachment `json:"attachments"`
}

// Mention.Type is one of "bot", "user" or "all".
type Mention struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type Attachment struct {
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
	Name string `json:"name,omitempty"`
	MIME string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
}

type EventAccepted struct {
	EventID          string `json:"event_id,omitempty"`
	Disposition      string `json:"disposition,omitempty"`
	TaskID           string `json:"task_id,omitempty"`
	Status           string `json:"status,omitempty"`
	SuppressedReason string `json:"suppressed_reason,omitempty"`
	Merged           bool   `json:"merged,omitempty"`
}

type Task struct {
	TaskID  string     `json:"task_id"`
	Status  string     `json:"status"`
	Actions []Action   `json:"actions"`
	Meta    TaskMeta   `json:"meta"`
	Error   *ErrorBody `json:"error,omitempty"`
}

type TaskMeta struct {
	SelectedModelRole string `json:"selected_model_role,omitempty"`
	SelectedModel     string `json:"selected_model,omitempty"`
	RouteReason       string `json:"route_reason,omitempty"`
	ReviewResult      string `json:"review_result,omitempty"`
	TraceID           string `json:"trace_id,omitempty"`
}

type Action struct {
	ActionID                 string        `json:"action_id"`
	Type                     string        `json:"type"`
	ConversationID           string        `json:"conversation_id"`
	ConversationType         string        `json:"conversation_type"`
	PlatformConversationID   string        `json:"platform_conversation_id"`
	ReplyToMessageID         string        `json:"reply_to_message_id,omitempty"`
	ReplyToPlatformMessageID string        `json:"reply_to_platform_message_id,omitempty"`
	Content                  ActionContent `json:"content"`
	Attachments              []Attachment  `json:"attachments,omitempty"`
	CreatedAt                time.Time     `json:"created_at"`
}

type ActionContent struct {
	Text string `json:"text"`
}

type OutboxResponse struct {
	Actions []Action `json:"actions"`
}

type AckRequest struct {
	Status            string `json:"status"`
	PlatformMessageID string `json:"platform_message_id,omitempty"`
	Error             string `json:"error,omitempty"`
}

type ConversationRef struct {
	Type       string `json:"type" yaml:"type"`
	PlatformID string `json:"platform_id" yaml:"platform_id"`
}

type Capabilities struct {
	SchemaVersion        string            `json:"schema_version"`
	BotInstanceID        string            `json:"bot_instance_id"`
	BotName              string            `json:"bot_name"`
	PlatformSelfID       string            `json:"platform_self_id"`
	Scopes               []string          `json:"scopes"`
	EventTypes           []string          `json:"event_types"`
	ActionTypes          []string          `json:"action_types"`
	TriggerTypes         []string          `json:"trigger_types"`
	EnabledConversations []ConversationRef `json:"enabled_conversations"`
	PrivateChat          string            `json:"private_chat"`
}
