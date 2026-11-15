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

// ChannelPanel and PlatformPanel are conloomapi.ChannelPanel and
// conloomapi.PlatformPanel.
const (
	ChannelPanel  = conloomapi.ChannelPanel
	PlatformPanel = conloomapi.PlatformPanel
)

// BotInstance is a registered bot: a config bot or a panel bot, deleted
// panel bots included.
type BotInstance struct {
	ID       string
	Platform string
	Name     string
}

// PanelBot is a bot created in the panel; it only has panel chats. Skills
// lists skill names, PersonaTemplate the default persona template of its new
// chats.
type PanelBot struct {
	ID              string
	Name            string
	PersonaTemplate string
	Skills          []string
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

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
	Channel           string // "" or ChannelPanel
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
	Channel           string    // "" or ChannelPanel
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

// PersonaImage is an uploaded appearance reference image, as checked by
// imaging.Process: the original bytes and a smaller JPEG reply copy. ID is
// the lowercase hex sha256 of Original, so its bytes never change; CreatedAt
// is when it was last uploaded.
type PersonaImage struct {
	ID          string
	MIME        string
	Width       int
	Height      int
	Original    []byte
	ReplyJPEG   []byte
	ReplyWidth  int
	ReplyHeight int
	ReplyTokens int
	CreatedBy   string
	CreatedAt   time.Time
}

// PersonaTemplateRecord is a stored persona template.
type PersonaTemplateRecord struct {
	Name      string
	Persona   conloomapi.PersonaDoc
	UpdatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UnreadablePersonaTemplate is a stored persona template row that could not
// be read, such as one whose persona is not valid JSON, and why.
type UnreadablePersonaTemplate struct {
	Name string
	Err  error
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
	// ProactivePrivateInitiate and ProactivePrivateFollowUp record a message
	// the bot started in a private chat, ProactivePrivateSkip one it chose not
	// to send, ProactivePrivateReturn a message it sent when a status ended.
	ProactivePrivateInitiate = "private_initiate"
	ProactivePrivateFollowUp = "private_followup"
	ProactivePrivateSkip     = "private_skip"
	ProactivePrivateReturn   = "private_return"
)

// ProactiveLogEntry records one proactive evaluation, sent reply or message
// the bot started or skipped in a private chat. Kind is one of the Proactive*
// constants.
type ProactiveLogEntry struct {
	ConversationKey string
	PlatformUserID  string
	Kind            string
	TaskID          string
	CreatedAt       time.Time
}

// GroupMemory is a memory scope's rolling summary and how far consolidation
// has read; CursorSeq is the last consolidated messages.seq. ConversationKey
// is the scope's key (see MemoryScopeKey), the conversation's own but for
// panel chats.
type GroupMemory struct {
	ConversationKey string
	Summary         string
	CursorSeq       int64
	LastRunAt       time.Time
	LastError       string
	UpdatedAt       time.Time
}

// MemberMemory is what the bot remembers about one user in one memory scope
// (ConversationKey, see MemoryScopeKey). A nil Affinity means none yet. Day fields hold local dates as "2006-01-02",
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
// Ingest returns ErrReplyHandedOut. FireTimer, when set, deletes that timer;
// if it was cancelled, re-armed or postponed meanwhile, nothing is written and
// Ingest returns ErrTimerGone.
type Ingest struct {
	Event           EventRecord
	Conversation    *ConversationRecord
	User            *UserRecord
	Message         *MessageRecord
	NewTask         *TaskRecord
	MergeIntoTaskID string
	CancelTaskID    string
	AbandonReplyOf  string
	FireTimer       *TimerRef
}

// TimerKind is the kind of a conversation timer. A conversation has at most
// one timer of each kind.
type TimerKind string

const (
	TimerStatusReturn TimerKind = "status_return"
	TimerInitiate     TimerKind = "initiate"
	TimerFollowUp     TimerKind = "follow_up"
)

// ConversationTimer is a durable wake-up of a private conversation at DueAt.
// AnchorSeq is the conversation's newest message seq when it was armed. A
// claimed timer has LeaseUntil in the future; zero means unclaimed.
type ConversationTimer struct {
	ConversationKey string
	Kind            TimerKind
	BotInstanceID   string
	PlatformUserID  string
	DueAt           time.Time
	AnchorSeq       int64
	LeaseUntil      time.Time
	CreatedAt       time.Time
}

// TimerRef names one arming of a conversation's timer: re-arming or
// postponing it changes DueAt, so an older reference no longer matches.
type TimerRef struct {
	ConversationKey string
	Kind            TimerKind
	DueAt           time.Time
}

// ConversationStatus is what the bot said it is doing in a private
// conversation, from StartedAt until UntilAt. TaskID is the task that set it.
type ConversationStatus struct {
	ConversationKey string
	Activity        string
	TaskID          string
	StartedAt       time.Time
	UntilAt         time.Time
	UpdatedAt       time.Time
}

// BuffValue is a hidden bar's value in a private conversation as last
// written at UpdatedAt, before any decay since. DayDelta is the bar's net
// change on Day, a local date as "2006-01-02".
type BuffValue struct {
	ConversationKey string
	Name            string
	Value           float64
	Day             string
	DayDelta        int
	UpdatedAt       time.Time
}

const (
	BuffSourceModel = "model"
	BuffSourcePanel = "panel"
	BuffSourceAPI   = "api"
)

// BuffEvent records one change of a bar from Before to After. Source is one
// of the BuffSource* constants; TaskID is the task that made the change,
// empty for none.
type BuffEvent struct {
	ID              int64
	ConversationKey string
	Name            string
	TaskID          string
	Source          string
	Before          float64
	After           float64
	Reason          string
	CreatedAt       time.Time
}
