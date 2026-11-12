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

// Ingest is everything written for one incoming event. Nil parts are skipped.
// MergeIntoTaskID, when set, appends Message.ID to that task's trigger messages.
type Ingest struct {
	Event           EventRecord
	Conversation    *ConversationRecord
	User            *UserRecord
	Message         *MessageRecord
	NewTask         *TaskRecord
	MergeIntoTaskID string
}
