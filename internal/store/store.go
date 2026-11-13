// Package store defines the persistence interface used by the ConLoom core.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// MessageHit is a message found by SearchMessages. A higher Score is more
// relevant; scores are only comparable within one search.
type MessageHit struct {
	Seq   int64
	Score float64
}

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
	// GetGroupMemory returns ErrNotFound when the conversation has no memory row.
	GetGroupMemory(ctx context.Context, convKey string) (GroupMemory, error)
	// PutGroupMemory inserts g or replaces the conversation's row; UpdatedAt is set to now.
	PutGroupMemory(ctx context.Context, g GroupMemory) error
	// GetMemberMemories maps each of userIDs that has a row to its memory.
	GetMemberMemories(ctx context.Context, convKey string, userIDs []string) (map[string]MemberMemory, error)
	// ListMemberMemories returns the conversation's member rows ordered by user ID.
	ListMemberMemories(ctx context.Context, convKey string) ([]MemberMemory, error)
	// ApplyMemoryWrite writes w in one transaction. A member is written only if
	// its stored version equals m.Version (0 for no row) and then gets the next
	// version; mismatched members are skipped with their revisions and returned
	// as conflicts. A broken summary guard keeps the stored summary (see
	// MemoryWrite), and the cursor never moves back. Parts keyed to another conversation, or a member listed
	// twice, fail the whole write.
	ApplyMemoryWrite(ctx context.Context, w MemoryWrite) (conflicts []string, err error)
	// ForgetPrivate forgets the user of a private conversation in one
	// transaction: what ForgetMember does, plus emptying the conversation's
	// summary, deleting its summary revisions and moving its cursor up to the
	// newest message, so nothing said before (by either side) is consolidated
	// again. A conversation without a memory row gets one.
	ForgetPrivate(ctx context.Context, convKey, userID string, at time.Time) error
	// ForgetMember clears the user's memory, marks it forgotten at at (creating
	// the row if needed) and deletes the user's revisions in the conversation.
	ForgetMember(ctx context.Context, convKey, userID string, at time.Time) error
	// ListMemoryRevisions returns the user's revisions oldest first; an empty
	// userID lists summary revisions.
	ListMemoryRevisions(ctx context.Context, convKey, userID string) ([]MemoryRevision, error)
	// PurgeMemoryRevisions deletes revisions created before before.
	PurgeMemoryRevisions(ctx context.Context, before time.Time) (int, error)
	// MessagesAfter returns up to limit messages with Seq > afterSeq ordered by
	// Seq ascending (none when limit <= 0).
	MessagesAfter(ctx context.Context, convKey string, afterSeq int64, limit int) ([]MessageRecord, error)
	// CountMessagesAfter counts the conversation's messages with Seq > afterSeq.
	CountMessagesAfter(ctx context.Context, convKey string, afterSeq int64) (int, error)
	// BacklogCursor returns the seq after which exactly backlog of the
	// conversation's messages remain, or 0 when it has no more than backlog.
	BacklogCursor(ctx context.Context, convKey string, backlog int) (int64, error)
	// UserIDsForActors maps each of actorIDs to the platform user ID of its
	// newest member message in the conversation; actors without one are absent.
	UserIDsForActors(ctx context.Context, convKey string, actorIDs []string) (map[string]string, error)
	// ListConversations returns all conversations of convType ordered by key.
	ListConversations(ctx context.Context, convType string) ([]ConversationRecord, error)
	// SearchMessages finds up to limit of the conversation's messages, bot
	// messages included, whose text or sender name holds any of terms
	// (search.Terms) and that occurred within r (After inclusive, Before
	// exclusive), best first by bm25 and newest first among equals.
	SearchMessages(ctx context.Context, convKey string, terms []string, r search.Range, limit int) ([]MessageHit, error)
	// MessagesAround returns the conversation's messages with fromSeq <= Seq <=
	// toSeq, plus up to before messages preceding them and up to after
	// following them, ordered by Seq ascending.
	MessagesAround(ctx context.Context, convKey string, fromSeq, toSeq int64, before, after int) ([]MessageRecord, error)
	// BackfillMessageIndex adds up to batch messages missing from the keyword
	// index, oldest first, and reports how many it added and how many
	// messages remain after its progress mark. It can be stopped and resumed
	// at any time, and also picks up messages written by binaries without the
	// index.
	BackfillMessageIndex(ctx context.Context, batch int) (indexed, remaining int, err error)
	Backup(ctx context.Context, path string) error
	Close() error
}
