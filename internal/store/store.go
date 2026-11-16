// Package store defines the persistence interface used by the ConLoom core.
package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// MessageHit is a message found by SearchMessages or SearchMessagesIn. A
// higher Score is more relevant; scores are only comparable within one
// search. ConversationKey is the message's conversation, set by
// SearchMessagesIn only.
type MessageHit struct {
	ConversationKey string
	Seq             int64
	Score           float64
}

var (
	ErrNotFound         = errors.New("store: not found")
	ErrDuplicateMessage = errors.New("store: duplicate platform message")
	// ErrTaskFinished, ErrReplyHandedOut and ErrTimerGone refuse an Ingest
	// (see Ingest.CancelTaskID, Ingest.AbandonReplyOf and Ingest.FireTimer);
	// ErrTaskFinished also refuses a SetTaskStatus.
	ErrTaskFinished   = errors.New("store: task already finished")
	ErrReplyHandedOut = errors.New("store: reply already handed out")
	ErrTimerGone      = errors.New("store: timer gone")
	// ErrBuffDuplicate refuses an UpdateBuff of a bar its task already
	// adjusted, and a RemoveCustomBar or RemoveEffect of a name its task
	// already removed.
	ErrBuffDuplicate = errors.New("store: already changed this way by this task")
	// ErrBuffNameTaken refuses an AddCustomBar or AddEffect of a name another
	// task's bar or active effect holds; ErrBuffLimit one over its limit.
	ErrBuffNameTaken = errors.New("store: buff name already in use")
	ErrBuffLimit     = errors.New("store: too many buffs")
	// ErrBotHasData refuses a DeletePanelBot of a bot that still has
	// conversations, tasks or outbox actions.
	ErrBotHasData = errors.New("store: bot still has conversations")
	// ErrAlreadyMigrated refuses a migration of the config file's persona
	// templates or bot settings into a database they were already migrated
	// into.
	ErrAlreadyMigrated = errors.New("store: already migrated from the config file")
)

// MetaConfigTemplatesMigrated is the database fact (app_meta key) holding
// when the config file's persona templates were migrated into the database.
const MetaConfigTemplatesMigrated = "config_templates_migrated"

// MetaConfigBotSettingsMigrated is the database fact (app_meta key) holding
// when the settings of the config file's bots were migrated into the
// database (see BotSettings).
const MetaConfigBotSettingsMigrated = "config_bot_settings_migrated"

// MetaPanelMemoryScoped is the database fact (app_meta key) holding when the
// memory of panel chats, kept per chat by older versions, was merged into
// their bots' shared scopes (see MemoryScopeKey).
const MetaPanelMemoryScoped = "panel_memory_scoped"

// ConversationKey returns the canonical "bot:platform:type:id" key of a conversation.
func ConversationKey(botID, platform, convType, convID string) string {
	return botID + ":" + platform + ":" + convType + ":" + convID
}

// ConversationType returns the type part of the conversation key key, "" when
// key is not a conversation key.
func ConversationType(key string) string {
	parts := strings.SplitN(key, ":", 4)
	if len(parts) != 4 {
		return ""
	}
	return parts[2]
}

type Store interface {
	UpsertBotInstance(ctx context.Context, id, platform, name string) error
	// ListBotInstances returns all registered bots ordered by ID.
	ListBotInstances(ctx context.Context) ([]BotInstance, error)
	// CreatePanelBot stores b and, in the same transaction, its bot instance
	// with platform PlatformPanel. It fails when a panel bot or bot instance
	// with b.ID exists, even one of a deleted panel bot. Nil Skills are stored
	// as none; a zero CreatedAt or UpdatedAt as now.
	CreatePanelBot(ctx context.Context, b PanelBot) error
	// UpdatePanelBot stores b's Name, PersonaTemplate, Skills and UpdatedAt (a
	// zero UpdatedAt as now) and, in the same transaction, the new name of its
	// bot instance; CreatedBy and CreatedAt are kept. ErrNotFound if the panel
	// bot does not exist.
	UpdatePanelBot(ctx context.Context, b PanelBot) error
	// ListPanelBots returns all panel bots ordered by CreatedAt, then ID.
	ListPanelBots(ctx context.Context) ([]PanelBot, error)
	// DeletePanelBot deletes the panel bot together with its leftover users
	// and events in one transaction; its bot instance, model calls and audit
	// events are kept. ErrNotFound if the panel bot does not exist,
	// ErrBotHasData, with nothing deleted, while it still has conversations,
	// tasks or outbox actions.
	DeletePanelBot(ctx context.Context, id string) error
	// GetBotSettings returns the stored settings of the config bot id,
	// ErrNotFound without any.
	GetBotSettings(ctx context.Context, id string) (BotSettings, error)
	// ListBotSettings returns the stored settings of config bots ordered by
	// bot ID.
	ListBotSettings(ctx context.Context) ([]BotSettings, error)
	// PutBotSettings stores s as the settings of the config bot
	// s.BotInstanceID, replacing its stored ones but keeping their CreatedAt,
	// and, in the same transaction, s.Name as the name of the bot's instance
	// when it has one. Nil lists are stored as empty; a zero UpdatedAt as now
	// and a zero CreatedAt of new settings as UpdatedAt.
	PutBotSettings(ctx context.Context, s BotSettings) error
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
	// SetTaskStatus sets the status of a queued or running task; a finished
	// one is left alone and gives ErrTaskFinished.
	SetTaskStatus(ctx context.Context, taskID, status string) error
	// CompleteTask updates the task and inserts its actions in one transaction.
	CompleteTask(ctx context.Context, c TaskCompletion) error
	// ListUnfinishedTasks returns tasks that are queued or running.
	ListUnfinishedTasks(ctx context.Context) ([]TaskRecord, error)
	ActionsForTask(ctx context.Context, taskID string) ([]conloomapi.Action, error)
	PurgeEvents(ctx context.Context, before time.Time) (int, error)
	// LeaseOutbox leases the bot's due actions of the platform channel (""):
	// per conversation only the head of that channel.
	LeaseOutbox(ctx context.Context, botID string, limit int, now time.Time, lease time.Duration) ([]OutboxRecord, error)
	// LeasePanelOutbox leases due actions with channel "panel" across all bots: per
	// conversation only the head of that channel, same not_before/lease rules as LeaseOutbox.
	LeasePanelOutbox(ctx context.Context, limit int, now time.Time, lease time.Duration) ([]OutboxRecord, error)
	// PendingPanelActions counts the conversation's pending or leased panel-channel
	// actions not yet expired at now.
	PendingPanelActions(ctx context.Context, convKey string, now time.Time) (int, error)
	GetOutbox(ctx context.Context, botID, actionID string) (OutboxRecord, error)
	AckOutbox(ctx context.Context, botID, actionID string, ack conloomapi.AckRequest, botMsg *MessageRecord, now time.Time) (alreadyAcked bool, err error)
	ExpireOutbox(ctx context.Context, now time.Time) (int, error)
	// CancelPendingActions marks all of the task's pending (never leased)
	// actions cancelled and returns how many it marked. Leased and finished
	// actions are left as they are.
	CancelPendingActions(ctx context.Context, taskID string, now time.Time) (int, error)
	// LastReplyTask returns the ID of the reply task (not a command) of
	// actorID whose actions were stored last in the conversation, or
	// ErrNotFound if no such task has actions.
	LastReplyTask(ctx context.Context, botID, convKey, actorID string) (string, error)
	// PurgeFinishedTasks deletes, in one transaction, up to limit tasks that
	// are succeeded, failed or cancelled and were last updated before before,
	// together with all their outbox actions. A task with a pending or leased
	// action is kept. It returns the numbers of deleted tasks and actions.
	PurgeFinishedTasks(ctx context.Context, before time.Time, limit int) (tasks, actions int, err error)
	// PurgeOutbox deletes up to limit acked, expired or cancelled actions
	// created before before whose task no longer exists.
	PurgeOutbox(ctx context.Context, before time.Time, limit int) (int, error)
	// PurgeAudit deletes up to limit audit events created before before.
	PurgeAudit(ctx context.Context, before time.Time, limit int) (int, error)
	// PurgeModelCalls deletes up to limit model calls made before before.
	PurgeModelCalls(ctx context.Context, before time.Time, limit int) (int, error)
	// PurgePanelSessions deletes panel sessions with ExpiresAt <= expiredBefore
	// or LastSeenAt <= idleBefore.
	PurgePanelSessions(ctx context.Context, expiredBefore, idleBefore time.Time) (int, error)
	LatestPersona(ctx context.Context, convKey string) (PersonaVersion, error)
	GetPersonaVersion(ctx context.Context, convKey string, version int) (PersonaVersion, error)
	// AppendPersonaVersion assigns the next version number.
	AppendPersonaVersion(ctx context.Context, pv PersonaVersion) (PersonaVersion, error)
	// PutPersonaImage stores img. If an image with its ID exists, only its
	// CreatedBy and CreatedAt are replaced, which restarts its purge grace
	// period; its bytes and sizes are kept. Nil Original or ReplyJPEG are
	// stored as empty, a zero CreatedAt as now.
	PutPersonaImage(ctx context.Context, img PersonaImage) error
	// GetPersonaImage returns the image, with its Original only when original
	// is set (nil otherwise), or ErrNotFound.
	GetPersonaImage(ctx context.Context, id string, original bool) (PersonaImage, error)
	PersonaImageExists(ctx context.Context, id string) (bool, error)
	// PurgePersonaImages deletes the images created before createdBefore that
	// are not in keep and that no persona template and no persona version, of
	// any age, names as its appearance image, and returns how many it deleted.
	// A row whose persona is not valid JSON references no image. keep lists
	// further images to keep.
	PurgePersonaImages(ctx context.Context, createdBefore time.Time, keep []string) (int, error)
	// ListPersonaTemplates returns the stored persona templates ordered by
	// Name. A row it cannot read does not fail the list: it is left out of
	// templates and reported in unreadable, also ordered by Name.
	ListPersonaTemplates(ctx context.Context) (templates []PersonaTemplateRecord, unreadable []UnreadablePersonaTemplate, err error)
	// PutPersonaTemplate stores r, replacing the template of the same name but
	// keeping its CreatedAt. A zero UpdatedAt is stored as now, a zero
	// CreatedAt as UpdatedAt.
	PutPersonaTemplate(ctx context.Context, r PersonaTemplateRecord) error
	// ImportPersonaTemplates stores images as PutPersonaImage does and then
	// rows as PutPersonaTemplate does, all in one transaction: when one
	// fails, nothing is stored.
	ImportPersonaTemplates(ctx context.Context, images []PersonaImage, rows []PersonaTemplateRecord) error
	// DeletePersonaTemplate deletes the template and reports whether it existed.
	DeletePersonaTemplate(ctx context.Context, name string) (bool, error)
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
	// DeleteProactiveLog deletes the conversation's entries recorded for
	// taskID and returns how many it deleted.
	DeleteProactiveLog(ctx context.Context, convKey, taskID string) (int, error)
	PurgeProactiveLog(ctx context.Context, before time.Time) (int, error)
	// GetGroupMemory, PutGroupMemory, GetMemberMemories, ListMemberMemories,
	// ApplyMemoryWrite, ForgetMember and ListMemoryRevisions key memory by its
	// scope (see MemoryScopeKey): where they say conversation they mean the
	// scope its caller mapped the conversation's key to.

	// GetGroupMemory returns ErrNotFound when the conversation has no memory row.
	GetGroupMemory(ctx context.Context, convKey string) (GroupMemory, error)
	// PutGroupMemory inserts g when the conversation has no memory row, and
	// otherwise only moves the row's cursor forward to g.CursorSeq, keeping
	// the rest, so that a row read before a forget cannot undo it; UpdatedAt
	// is set to now.
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
	// transaction: what ForgetMember does to the member, plus emptying the
	// conversation's summary, deleting its summary revisions and moving its
	// cursor up to the newest message, so nothing said before (by either side)
	// is consolidated again. A conversation without a memory row gets one. Its
	// semantic search chunks that start before at are deleted, and those
	// running past at queued to be rebuilt. The conversation's status, timers,
	// bar values, bar changes, bot-created bars and effects are deleted too.
	// It is ForgetPrivateScope of the conversation as its own scope and only
	// chat.
	ForgetPrivate(ctx context.Context, convKey, userID string, at time.Time) error
	// ForgetPrivateScope is ForgetPrivate for the memory scope scopeKey (see
	// MemoryScopeKey) of the private chats chats: the member, summary, summary
	// revisions, cursor, status, bar values, bar changes, bot-created bars and
	// effects are those under scopeKey, the cursor moves up to the newest
	// message of any of chats, and the chunks and timers of each of chats are
	// dealt with as ForgetPrivate deals with its conversation's.
	ForgetPrivateScope(ctx context.Context, scopeKey string, chats []string, userID string, at time.Time) error
	// ForgetMember clears the user's memory, marks it forgotten at at (creating
	// the row if needed) and deletes the user's revisions in the conversation.
	// The conversation's semantic search chunks holding a message the user sent
	// before at are deleted and queued to be rebuilt without it.
	ForgetMember(ctx context.Context, convKey, userID string, at time.Time) error
	// ListMemoryRevisions returns the user's revisions oldest first; an empty
	// userID lists summary revisions.
	ListMemoryRevisions(ctx context.Context, convKey, userID string) ([]MemoryRevision, error)
	// PurgeMemoryRevisions deletes revisions created before before.
	PurgeMemoryRevisions(ctx context.Context, before time.Time) (int, error)
	// MessagesAfter returns up to limit messages with Seq > afterSeq ordered by
	// Seq ascending (none when limit <= 0).
	MessagesAfter(ctx context.Context, convKey string, afterSeq int64, limit int) ([]MessageRecord, error)
	// MessagesAfterIn is MessagesAfter over the messages of all of convKeys
	// together (none without convKeys).
	MessagesAfterIn(ctx context.Context, convKeys []string, afterSeq int64, limit int) ([]MessageRecord, error)
	// CountMessagesAfter counts the conversation's messages with Seq > afterSeq.
	CountMessagesAfter(ctx context.Context, convKey string, afterSeq int64) (int, error)
	// CountMessagesAfterIn is CountMessagesAfter over all of convKeys.
	CountMessagesAfterIn(ctx context.Context, convKeys []string, afterSeq int64) (int, error)
	// BacklogCursor returns the seq after which exactly backlog of the
	// conversation's messages remain, or 0 when it has no more than backlog.
	BacklogCursor(ctx context.Context, convKey string, backlog int) (int64, error)
	// BacklogCursorIn is BacklogCursor over the messages of all of convKeys
	// together.
	BacklogCursorIn(ctx context.Context, convKeys []string, backlog int) (int64, error)
	// LatestChat returns which of convKeys has the newest message, or "" when
	// none of them has a message.
	LatestChat(ctx context.Context, convKeys []string) (string, error)
	// PanelChatKeys returns the keys of the bot's panel chats (PlatformPanel,
	// private), which share its panel memory scope, ordered by key.
	PanelChatKeys(ctx context.Context, botID string) ([]string, error)
	// UserIDsForActors maps each of actorIDs to the platform user ID of its
	// newest member message in the conversation; actors without one are absent.
	UserIDsForActors(ctx context.Context, convKey string, actorIDs []string) (map[string]string, error)
	// ListConversations returns all conversations of convType ordered by key.
	ListConversations(ctx context.Context, convType string) ([]ConversationRecord, error)
	// SearchMessages finds the conversation's messages, bot messages
	// included, whose text or sender name holds any of terms (search.Terms)
	// and that occurred within r (After inclusive, Before exclusive), best
	// first by bm25 and newest first among equals, and returns up to limit of
	// them after skipping offset.
	SearchMessages(ctx context.Context, convKey string, terms []string, r search.Range, offset, limit int) ([]MessageHit, error)
	// SearchMessagesIn is SearchMessages over the messages of all of convKeys
	// together (the chats of a shared memory scope), each hit with its
	// ConversationKey; none without convKeys.
	SearchMessagesIn(ctx context.Context, convKeys []string, terms []string, r search.Range, offset, limit int) ([]MessageHit, error)
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
	// SetStatus stores s as the only status of s.ConversationKey, a memory
	// scope (MemoryScopeKey; the status, bar and bar change methods below take
	// scope keys where they say conversation, while timers stay with each
	// chat), set in the chat s.Origin(), and, in the same transaction, ret as
	// that chat's status_return timer (ConversationKey and Kind may be left
	// empty), or deletes that timer when ret is nil. When the status it
	// replaces was set in another chat, that chat's status_return timer is
	// deleted too.
	SetStatus(ctx context.Context, s ConversationStatus, ret *ConversationTimer) error
	// GetStatus returns the conversation's status, expired or not, or ErrNotFound.
	GetStatus(ctx context.Context, convKey string) (ConversationStatus, error)
	// ClearStatus deletes the conversation's status if its UntilAt equals
	// untilAt (any status for a zero untilAt), together with the
	// status_return timer of the chat it was set in, and reports whether a
	// status was deleted. A zero untilAt deletes convKey's timer even without
	// a status.
	ClearStatus(ctx context.Context, convKey string, untilAt time.Time) (bool, error)
	// ArmTimer stores t, replacing the conversation's timer of the same kind,
	// unclaimed.
	ArmTimer(ctx context.Context, t ConversationTimer) error
	// CancelTimers deletes the conversation's timers of kinds (none without
	// kinds) and returns how many it deleted.
	CancelTimers(ctx context.Context, convKey string, kinds ...TimerKind) (int, error)
	// ListTimers returns the conversation's timers ordered by DueAt.
	ListTimers(ctx context.Context, convKey string) ([]ConversationTimer, error)
	// ClaimDueTimers leases, until now+lease, up to limit timers due at or
	// before now that are unclaimed or whose lease ran out before now, and
	// returns them ordered by DueAt.
	ClaimDueTimers(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]ConversationTimer, error)
	// PostponeTimer moves the timer ref names to dueAt, unclaimed, and reports
	// whether it did; a timer cancelled, re-armed or postponed since is left as
	// it is.
	PostponeTimer(ctx context.Context, ref TimerRef, dueAt time.Time) (bool, error)
	// DeleteTimer deletes the timer ref names and reports whether it did; a
	// timer cancelled, re-armed or postponed since is left as it is.
	DeleteTimer(ctx context.Context, ref TimerRef) (bool, error)
	// UserMessageTimes returns when the conversation's newest up to limit
	// member (not bot) messages occurred at or after since, newest first (none
	// when limit <= 0). It looks at no more than the conversation's newest
	// 4×limit messages, bot messages included, so fewer may be returned even
	// when older member messages would match.
	UserMessageTimes(ctx context.Context, convKey string, since time.Time, limit int) ([]time.Time, error)
	// GetBuffs returns the conversation's stored bar values ordered by Name,
	// including bars its persona no longer has.
	GetBuffs(ctx context.Context, convKey string) ([]BuffValue, error)
	// UpdateBuff changes the conversation's bar name in one transaction. A
	// task makes each kind of change of a name at most once: if ev.TaskID is
	// set and that task already adjusted (BuffKindAdjust) a bar of that name,
	// nothing is written and it returns ErrBuffDuplicate; its other changes of
	// the name, such as adding the bar, do not count.
	// Otherwise it calls apply with the stored value (zero and found false
	// without one), stores and returns apply's next under convKey and name (a
	// zero UpdatedAt as now), and records ev under convKey and name as a
	// BuffKindAdjust with Before apply's before (the value next replaced, such
	// as the stored value decayed to now), After next.Value and a zero
	// CreatedAt as now; ev.Kind, ev.Before and ev.After are not used. When
	// apply fails nothing is written and its error is returned.
	UpdateBuff(ctx context.Context, convKey, name string, ev BuffEvent,
		apply func(cur BuffValue, found bool) (next BuffValue, before float64, err error)) (BuffValue, error)
	// ListBuffEvents returns the conversation's newest up to limit bar changes,
	// newest first (none when limit <= 0).
	ListBuffEvents(ctx context.Context, convKey string, limit int) ([]BuffEvent, error)
	// PurgeBuffEvents deletes bar changes created before before.
	PurgeBuffEvents(ctx context.Context, before time.Time) (int64, error)
	// AddCustomBar adds b to b.ConversationKey in one transaction, with its
	// value initial (a BuffValue at b.CreatedAt without day bookkeeping, which
	// replaces a stored value of that name) and a BuffKindBarAdd change by the
	// model and b.TaskID at b.CreatedAt whose Before and After are initial; a
	// zero CreatedAt is stored as now. When the task b.TaskID already added a
	// bar of that name (see UpdateBuff), or holds it, nothing is written and
	// it returns nil, so a re-run task adds it only once. Otherwise it returns
	// ErrBuffNameTaken when another bar or an effect active at CreatedAt holds
	// the name, and ErrBuffLimit when the conversation has limit bars already.
	// The persona's bars are the caller's to check.
	AddCustomBar(ctx context.Context, b CustomBar, initial float64, limit int) error
	// RemoveCustomBar deletes the conversation's bot-created bar name and,
	// unless keepValue (the persona has since gained a bar of that name, which
	// shows the stored value now), its value; records a BuffKindBarRemove
	// change by source and taskID at now (a zero now as the current time)
	// whose Before and After are the value as last stored, and reports whether
	// there was such a bar. When there is one but taskID already removed a bar
	// of that name (see UpdateBuff), nothing is written and it returns
	// ErrBuffDuplicate.
	RemoveCustomBar(ctx context.Context, convKey, name, source, taskID string, keepValue bool, now time.Time) (bool, error)
	// ListCustomBars returns the conversation's bot-created bars in the order
	// they were added.
	ListCustomBars(ctx context.Context, convKey string) ([]CustomBar, error)
	// AddEffect adds e to e.ConversationKey in one transaction, with a
	// BuffKindEffectAdd change by the model and e.TaskID at now; a zero
	// StartedAt is stored as now. An effect whose UntilAt is not after now has
	// run out: one of e's name is replaced. When the task e.TaskID already
	// added an effect of that name, or holds an active one, nothing is written
	// and it returns nil. Otherwise it returns ErrBuffNameTaken when a
	// bot-created bar or another active effect holds the name, and
	// ErrBuffLimit when limit effects are active.
	AddEffect(ctx context.Context, e Effect, limit int, now time.Time) error
	// RemoveEffect deletes the conversation's effect name, active or run out,
	// records a BuffKindEffectRemove change by source and taskID at now as
	// RemoveCustomBar does (ErrBuffDuplicate included), and reports whether
	// there was such an effect.
	RemoveEffect(ctx context.Context, convKey, name, source, taskID string, now time.Time) (bool, error)
	// ListEffects returns the conversation's effects active at now, ordered
	// by StartedAt.
	ListEffects(ctx context.Context, convKey string, now time.Time) ([]Effect, error)
	// PurgeExpiredEffects deletes the effects that have run out at now and
	// returns how many it deleted.
	PurgeExpiredEffects(ctx context.Context, now time.Time) (int, error)
	// DeleteConversation deletes everything stored under convKey (messages and their
	// keyword index rows, tasks, outbox actions, personas, policies, members, memory,
	// chunks, proactive log, status, timers, bars, effects, users only referenced by this
	// conversation, events of it) except model_calls and audit_events, then the
	// conversation row. For a panel chat it also deletes the status of its shared
	// memory scope when that was set in the chat; the rest of the scope stays.
	// ErrNotFound if absent.
	DeleteConversation(ctx context.Context, convKey string) error
	// DeleteScope deletes everything stored under the shared panel memory scope
	// scopeKey (summary and cursor, members with their forget times, revisions,
	// chunk records, status, timers, bar values, bar changes, bot-created bars
	// and effects), for a panel
	// bot whose chats are gone. It returns ErrBotHasData, with nothing deleted,
	// while the bot still has panel chats; a key that is not a shared scope
	// (IsSharedScope) is an error.
	DeleteScope(ctx context.Context, scopeKey string) error
	Backup(ctx context.Context, path string) error
	Close() error
}
