// Package store defines the persistence interface used by the ConLoom core.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

var (
	ErrNotFound         = errors.New("store: not found")
	ErrDuplicateMessage = errors.New("store: duplicate platform message")
)

// ConversationKey returns the canonical "bot:platform:type:id" key of a conversation.
func ConversationKey(botID, platform, convType, convID string) string {
	return botID + ":" + platform + ":" + convType + ":" + convID
}

type Store interface {
	UpsertBotInstance(ctx context.Context, id, platform, name string) error
	// Ingest writes an event and its derived records in one transaction. If the
	// event's idempotency key already exists nothing is written and the stored
	// event is returned as existing.
	Ingest(ctx context.Context, in Ingest) (existing *EventRecord, err error)
	GetEvent(ctx context.Context, botID, key string) (EventRecord, error)
	GetConversation(ctx context.Context, botID, conversationID string) (ConversationRecord, error)
	GetMessage(ctx context.Context, botID, id string) (MessageRecord, error)
	// GetMessages returns the found messages ordered by Seq ascending.
	GetMessages(ctx context.Context, botID string, ids []string) ([]MessageRecord, error)
	// RecentMessages returns the newest limit messages ordered by Seq ascending
	// (none when limit <= 0).
	RecentMessages(ctx context.Context, conversationKey string, limit int) ([]MessageRecord, error)
	// LatestDisplayNames maps each of actorIDs to the non-empty display name of
	// its newest member message in the conversation; actors without one are
	// absent.
	LatestDisplayNames(ctx context.Context, conversationKey string, actorIDs []string) (map[string]string, error)
	GetTask(ctx context.Context, botID, taskID string) (TaskRecord, error)
	SetTaskStatus(ctx context.Context, taskID, status string) error
	// CompleteTask updates the task and inserts its actions in one transaction.
	CompleteTask(ctx context.Context, c TaskCompletion) error
	// ListUnfinishedTasks returns tasks that are queued or running.
	ListUnfinishedTasks(ctx context.Context) ([]TaskRecord, error)
	ActionsForTask(ctx context.Context, taskID string) ([]conloomapi.Action, error)
	PurgeEvents(ctx context.Context, before time.Time) (int, error)
	LeaseOutbox(ctx context.Context, botID string, limit int, now time.Time, lease time.Duration) ([]OutboxRecord, error)
	GetOutbox(ctx context.Context, botID, actionID string) (OutboxRecord, error)
	AckOutbox(ctx context.Context, botID, actionID string, ack conloomapi.AckRequest, botMsg *MessageRecord, now time.Time) (alreadyAcked bool, err error)
	ExpireOutbox(ctx context.Context, now time.Time) (int, error)
	LatestPersona(ctx context.Context, convKey string) (PersonaVersion, error)
	GetPersonaVersion(ctx context.Context, convKey string, version int) (PersonaVersion, error)
	// AppendPersonaVersion assigns the next version number.
	AppendPersonaVersion(ctx context.Context, pv PersonaVersion) (PersonaVersion, error)
	LatestPolicy(ctx context.Context, convKey string) (PolicyVersion, error)
	// ListPolicyVersions returns versions newest first.
	ListPolicyVersions(ctx context.Context, convKey string) ([]PolicyVersion, error)
	AppendPolicyVersion(ctx context.Context, pv PolicyVersion) (PolicyVersion, error)
	InsertAudit(ctx context.Context, a AuditEvent) error
	GetMember(ctx context.Context, convKey, userID string) (MemberRecord, error)
	// PutMember inserts m or replaces the stored row with the same conversation and user.
	PutMember(ctx context.Context, m MemberRecord) error
	AppendProactiveLog(ctx context.Context, e ProactiveLogEntry) error
	// CountProactive counts kind entries created at or after since; an empty
	// userID counts every user in the conversation.
	CountProactive(ctx context.Context, convKey, userID, kind string, since time.Time) (int, error)
	PurgeProactiveLog(ctx context.Context, before time.Time) (int, error)
	Backup(ctx context.Context, path string) error
	Close() error
}
