package store

import "time"

// ConversationStats is a conversation with a summary of its messages.
type ConversationStats struct {
	ConversationRecord
	MessageCount  int
	LastMessageAt time.Time // zero when the conversation has no messages
}

// TaskFilter and AuditFilter list newest first; Before is an ID cursor
// (IDs are time-ordered, see internal/ids): only IDs < Before when non-empty.
// Empty strings do not filter.
type TaskFilter struct {
	Status, BotInstanceID, ConversationKey, Before string
	Limit                                          int
}

type AuditFilter struct {
	Kind, BotInstanceID, Before string
	Limit                       int
}
