package store

import (
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	OutboxPending   = "pending"
	OutboxLeased    = "leased"
	OutboxAcked     = "acked"
	OutboxExpired   = "expired"
	OutboxCancelled = "cancelled"
)

type ConversationRecord struct {
	BotInstanceID          string
	ID                     string
	Key                    string
	Type                   string
	Platform               string
	PlatformConversationID string
}

type UserRecord struct {
	BotInstanceID  string
	Platform       string
	PlatformUserID string
	DisplayName    string
}

type EventRecord struct {
	BotInstanceID  string
	IdempotencyKey string
	RequestHash    string
	EventID        string
	Response       []byte
	RawEvent       []byte
	CreatedAt      time.Time
}

type MessageRecord struct {
	Seq               int64
	ID                string
	BotInstanceID     string
	ConversationKey   string
	Platform          string
	PlatformMessageID string
	ActorID           string
	PlatformUserID    string
	DisplayName       string
	Text              string
	ReplyTo           string
	Mentions          []conloomapi.Mention
	Attachments       []conloomapi.Attachment
	FromBot           bool
	OccurredAt        time.Time
	CreatedAt         time.Time
}

type TaskRecord struct {
	ID                  string
	BotInstanceID       string
	ConversationKey     string
	Status              string
	ActorPlatformUserID string
	Command             string
	TriggerMessageIDs   []string
	Priority            int
	Meta                conloomapi.TaskMeta
	ErrorCode           string
	EventOccurredAt     time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// OutboxRecord is a stored action. Status is one of the Outbox* constants.
type OutboxRecord struct {
	Action            conloomapi.Action
	BotInstanceID     string
	ConversationKey   string
	TaskID            string
	Platform          string
	Status            string
	Seq               int64
	LeaseUntil        time.Time
	Attempts          int
	PlatformMessageID string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	NotBefore         time.Time // earliest lease time; zero means immediately
}

type TaskCompletion struct {
	TaskID    string
	Status    string
	Meta      conloomapi.TaskMeta
	ErrorCode string
	Actions   []OutboxRecord
}

type PersonaVersion struct {
	ConversationKey string
	TemplateName    string
	Version         int
	Persona         conloomapi.PersonaDoc
	Reason          string
	UpdatedBy       string
	CreatedAt       time.Time
}

type PolicyVersion struct {
	ConversationKey string
	Version         int
	Policy          conloomapi.GroupPolicy
	Reason          string
	UpdatedBy       string
	CreatedAt       time.Time
}

type AuditEvent struct {
	ID            string
	BotInstanceID string
	Kind          string
	Actor         string
	Target        string
	Detail        string
	CreatedAt     time.Time
}

// MemberRecord is a user's engagement state in one conversation. Interests
// maps profile terms to weights.
type MemberRecord struct {
	ConversationKey string
	PlatformUserID  string
	LastEngagedAt   time.Time
	ProactiveLevel  string
	LevelSetByUser  bool
	Interests       map[string]float64
	UpdatedAt       time.Time
}

const (
	ProactiveEvaluated = "evaluated"
	ProactiveSent      = "sent"
)

// ProactiveLogEntry records one proactive evaluation or sent reply. Kind is
// one of the Proactive* constants.
type ProactiveLogEntry struct {
	ConversationKey string
	PlatformUserID  string
	Kind            string
	TaskID          string
	CreatedAt       time.Time
}

// GroupMemory is a conversation's rolling summary and how far consolidation
// has read; CursorSeq is the last consolidated messages.seq.
type GroupMemory struct {
	ConversationKey string
	Summary         string
	CursorSeq       int64
	LastRunAt       time.Time
	LastError       string
	UpdatedAt       time.Time
}

// MemberMemory is what the bot remembers about one user in one conversation.
// A nil Affinity means none yet. Day fields hold local dates as "2006-01-02",
// empty when unset. Version increases on every write and is used for
// optimistic checks.
type MemberMemory struct {
	ConversationKey    string
	PlatformUserID     string
	DisplayName        string
	Note               string
	Affinity           *int
	LastInteractionDay string
	ModelDeltaDay      string
	ModelDeltaSum      int
	DecaySteps         int
	ForgottenAt        time.Time
	Version            int64
	UpdatedAt          time.Time
}

const (
	FieldSummary  = "summary"
	FieldNote     = "note"
	FieldAffinity = "affinity"
)

const (
	RevisionConsolidate = "consolidate"
	RevisionRule        = "rule"
	RevisionCommand     = "command"
	RevisionAPI         = "api"
	RevisionPanel       = "panel"
)

// MemoryRevision records one change to a memory field. Field is one of the
// Field* constants and Source one of the Revision* constants; summary
// revisions have an empty PlatformUserID.
type MemoryRevision struct {
	ID              int64
	ConversationKey string
	PlatformUserID  string
	Field           string
	OldValue        string
	NewValue        string
	Source          string
	Reason          string
	CreatedAt       time.Time
}

// MemoryWrite is everything written for one conversation's memory in one
// transaction. A nil Group is left unchanged. With KeepSummary, or when a
// member of SummaryGuards no longer has the stored version given (0 for no
// row), Group's summary is not written: the stored summary is kept (empty
// without a row), the rest of Group is written and summary revisions are
// dropped.
type MemoryWrite struct {
	ConversationKey string
	Group           *GroupMemory
	Members         []MemberMemory
	Revisions       []MemoryRevision
	KeepSummary     bool
	SummaryGuards   []MemberVersion
}

// MemberVersion is a member's memory version as read (0 for no row).
type MemberVersion struct {
	PlatformUserID string
	Version        int64
}

// Ingest is everything written for one incoming event. Nil parts are skipped.
// MergeIntoTaskID, when set, appends Message.ID to that task's trigger messages.
//
// CancelTaskID, when set, stores that task as cancelled with error code
// task_cancelled; if it is already finished, nothing is written and Ingest
// returns ErrTaskFinished. AbandonReplyOf, when set, cancels all pending
// actions of that task, as CancelPendingActions does at Event.CreatedAt; if
// any of its actions was handed out (leased or acked), nothing is written and
// Ingest returns ErrReplyHandedOut.
type Ingest struct {
	Event           EventRecord
	Conversation    *ConversationRecord
	User            *UserRecord
	Message         *MessageRecord
	NewTask         *TaskRecord
	MergeIntoTaskID string
	CancelTaskID    string
	AbandonReplyOf  string
}
