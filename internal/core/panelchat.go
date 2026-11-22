package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// panelTextMax is the most characters of a panel message.
	panelTextMax = 2000
	// panelNameMax is the most characters of a panel chat's display name.
	panelNameMax = 32
)

// SubmitPanel accepts text and images written in the panel by panelUser into
// the panel chat convKey (store.PlatformPanel), like Submit accepts an event:
// the message is stored on the panel channel and the bot's replies there are
// delivered to the panel only (see replyChannel). It speaks as
// panel_<panelUser>, under the display name the chat was created with, and
// returns the new message's ID and, when the message is stored but will not
// be answered, why (conloomapi.EventAccepted.SuppressedReason, such as
// paused, rate_limited or queue_full). images are IDs of images uploaded to
// convKey and not sent yet, at most conloomapi.MaxPanelMessageImages and
// distinct, else ErrBadImages (checked under the chat's panel lock; nothing
// is stored). They are stored in order as Attachment{Type: "image", ID: id,
// MIME: "image/jpeg"}, and the store claims them with the message; one purged
// before the claim is ErrBadImages too. Text of more than panelTextMax
// characters is ErrBadText, and so is blank text without images; with images
// it may be blank (stored as ""). A conversation on another platform is
// ErrNotPanelChat; an unknown conversation, also one deleted meanwhile (see
// DeletePanelChat), store.ErrNotFound; one whose bot is being deleted (see
// PanelBotService.Delete) ErrBotDeleting.
func (o *Orchestrator) SubmitPanel(ctx context.Context, convKey, panelUser, text string, images ...string) (messageID, suppressed string, err error) {
	botID, _, _ := strings.Cut(convKey, ":")
	if err := o.panelBots.notDeleting(botID); err != nil {
		return "", "", err
	}
	unlock := o.panelLocks.lock(convKey)
	defer unlock()
	// The bot may have been marked while the chat's lock was awaited.
	if err := o.panelBots.notDeleting(botID); err != nil {
		return "", "", err
	}
	conv, err := o.panelConversation(ctx, convKey)
	if err != nil {
		return "", "", err
	}
	if conv.Platform != store.PlatformPanel {
		return "", "", ErrNotPanelChat
	}
	if conv.Type != conloomapi.ConvPrivate {
		return "", "", ErrNotPrivate
	}
	blank := strings.TrimSpace(text) == ""
	if blank && len(images) == 0 || utf8.RuneCountInString(text) > panelTextMax {
		return "", "", ErrBadText
	}
	if blank {
		text = ""
	}
	attachments := []conloomapi.Attachment{}
	if len(images) > 0 {
		if err := o.unsentChatImages(ctx, convKey, images); err != nil {
			return "", "", err
		}
		for _, id := range images {
			attachments = append(attachments, conloomapi.Attachment{Type: "image", ID: id, MIME: "image/jpeg"})
		}
	}
	if _, ok := o.bots.Bot(conv.BotInstanceID); !ok {
		return "", "", fmt.Errorf("%w: unknown bot %q", store.ErrNotFound, conv.BotInstanceID)
	}
	actorID := PanelActorID(panelUser)
	pmid := ids.New("panel")
	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion, EventID: ids.New("evt"), EventType: conloomapi.EventTypeMessageCreated,
		BotInstanceID: conv.BotInstanceID, Platform: conv.Platform, OccurredAt: o.now().UTC(),
		Conversation: conloomapi.Conversation{Type: conv.Type, ID: conv.ID, PlatformConversationID: conv.PlatformConversationID},
		Actor: conloomapi.Actor{ID: actorID, PlatformUserID: actorID,
			DisplayName: o.panelDisplayName(ctx, conv, actorID, panelUser)},
		Trigger: conloomapi.Trigger{Type: conloomapi.TriggerPrivate, RequestedResponse: true},
		Message: conloomapi.Message{ID: "msg_" + pmid, PlatformMessageID: pmid, Text: text,
			Mentions: []conloomapi.Mention{}, Attachments: attachments},
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return "", "", err
	}
	resp, _, err := o.submitOn(ctx, conv.BotInstanceID, pmid, body, ev, store.ChannelPanel)
	if err != nil {
		// The cleanup loop purges unsent images without the panel lock: one
		// purged since the check fails the store's claim.
		if o.chatImagesTaken(ctx, convKey, images, ev.Message.ID) {
			return "", "", ErrBadImages
		}
		return "", "", err
	}
	return ev.Message.ID, resp.SuppressedReason, nil
}

// panelDisplayName is the name actorID goes by in the panel chat conv: the
// one of their newest message there or, before they sent one, the one the
// chat was created with (kept by its creation event) if they created it, or
// else their panel user name.
func (o *Orchestrator) panelDisplayName(ctx context.Context, conv store.ConversationRecord, actorID, panelUser string) string {
	names, err := o.st.LatestDisplayNames(ctx, conv.Key, []string{actorID})
	if err != nil {
		o.log.Warn("load panel display name failed", "conversation", conv.Key, "error", err)
	} else if name := names[actorID]; name != "" {
		return name
	}
	if rec, err := o.st.GetEvent(ctx, conv.BotInstanceID, conv.ID); err == nil {
		var ev conloomapi.Event
		if json.Unmarshal(rec.RawEvent, &ev) == nil && ev.Actor.ID == actorID && ev.Actor.DisplayName != "" {
			return ev.Actor.DisplayName
		}
	}
	return panelChatName("", panelUser)
}

// CreatePanelChat creates a private chat of the bot on the panel platform for
// panelUser, who goes by displayName there (trimmed, panelUser when empty, at
// most panelNameMax characters), and returns its conversation key. An unknown
// bot is store.ErrNotFound, one being deleted (see PanelBotService.Delete)
// ErrBotDeleting.
func (o *Orchestrator) CreatePanelChat(ctx context.Context, botID, panelUser, displayName string) (convKey string, err error) {
	unlock, err := o.panelBots.openChat(botID)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, ok := o.bots.Bot(botID); !ok {
		return "", fmt.Errorf("%w: unknown bot %q", store.ErrNotFound, botID)
	}
	convID := ids.New("panel")
	convKey = store.ConversationKey(botID, store.PlatformPanel, conloomapi.ConvPrivate, convID)
	actorID := PanelActorID(panelUser)
	name := panelChatName(displayName, panelUser)
	now := o.now()
	// The creation is stored as an event without a message under the
	// conversation ID, which keeps the display name until the first message
	// and goes with the conversation when it is deleted.
	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion, EventID: ids.New("evt"), BotInstanceID: botID,
		Platform: store.PlatformPanel, OccurredAt: now.UTC(),
		Conversation: conloomapi.Conversation{Type: conloomapi.ConvPrivate, ID: convID, PlatformConversationID: convID},
		Actor:        conloomapi.Actor{ID: actorID, PlatformUserID: actorID, DisplayName: name},
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	resp, err := json.Marshal(conloomapi.EventAccepted{EventID: ev.EventID, Disposition: conloomapi.DispositionObserve})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	_, err = o.st.Ingest(ctx, store.Ingest{
		Event: store.EventRecord{BotInstanceID: botID, IdempotencyKey: convID, RequestHash: hex.EncodeToString(sum[:]),
			EventID: ev.EventID, Response: resp, RawEvent: body, CreatedAt: now},
		Conversation: &store.ConversationRecord{BotInstanceID: botID, ID: convID, Key: convKey, Type: conloomapi.ConvPrivate,
			Platform: store.PlatformPanel, PlatformConversationID: convID},
		User: &store.UserRecord{BotInstanceID: botID, Platform: store.PlatformPanel, PlatformUserID: actorID, DisplayName: name},
	})
	if err != nil {
		return "", err
	}
	o.log.Info("panel chat created", "conversation", convKey)
	return convKey, nil
}

// DeletePanelChat deletes the panel chat convKey: it cancels the chat's
// queued and running tasks, waits up to panelDeleteWait for the running ones
// to stop, then cancels the chat's pending and running state steps and waits
// up to panelDeleteWait for the running one to return, drops its scheduler
// session, deletes what is stored under convKey
// (see store.Store.DeleteConversation: its history and the semantic search
// chunks made of it, and the status of the bot's shared memory scope if it
// was set in this chat, with its timer; the rest of the shared memory stays)
// and then forgets the chat's session, lull timer, rate-limit hits, cached
// persona prompt and policy, and reply suggestions.
// It holds the chat's panel lock throughout, so a SubmitPanel waits for it
// and then finds no conversation. If a task has not stopped in time nothing
// is deleted and ErrChatBusy is returned; the tasks stay cancelled, so a
// retry succeeds once they stopped. Thus no task of the chat runs after it is
// deleted to store or cache anything under its key again. A conversation not
// on store.PlatformPanel is ErrNotPanelChat, an unknown one store.ErrNotFound.
func (o *Orchestrator) DeletePanelChat(ctx context.Context, convKey string) error {
	unlock := o.panelLocks.lock(convKey)
	defer unlock()
	conv, err := o.panelConversation(ctx, convKey)
	if err != nil {
		return err
	}
	if conv.Platform != store.PlatformPanel {
		return ErrNotPanelChat
	}
	var running []<-chan struct{}
	for _, t := range o.sched.Active(convKey) {
		done, err := o.cancel(ctx, conv.BotInstanceID, convKey, t.TaskID)
		if err != nil {
			return err
		}
		if done != nil {
			running = append(running, done)
		}
	}
	// The session is kept until its tasks stopped, so a retry finds a task
	// still running among them.
	if err := o.awaitStopped(ctx, convKey, running); err != nil {
		return err
	}
	// Only once the tasks stopped has every turn of the chat reached the
	// state steps.
	if done := o.cancelStateSteps(convKey, ""); done != nil {
		if err := o.awaitStopped(ctx, convKey, []<-chan struct{}{done}); err != nil {
			return err
		}
	}
	o.sched.Forget(convKey)
	if err := o.st.DeleteConversation(ctx, convKey); err != nil {
		return err
	}
	o.forgetConversation(convKey)
	o.stopLull(convKey)
	o.limiter.Forget(convKey + ":")
	o.personas.Forget(convKey)
	o.policies.Forget(convKey)
	o.suggestions.forget(convKey)
	o.log.Info("panel chat deleted", "conversation", convKey)
	return nil
}

// holdWakes keeps wake tasks of the chat convKey from starting until end is
// called, as accepting a message of its user does (see submit): fireTimer
// starts none from now on, and one it was starting already is queued when
// holdWakes returns, so a busy check after it sees that task. Unlike a
// message it cancels no wake task.
func (o *Orchestrator) holdWakes(convKey string) (end func()) {
	end = o.speakers.begin(convKey)
	unlock := o.proactiveLocks.lock(convKey)
	unlock()
	return end
}

// panelBusy reports whether the panel chat cannot be changed right now: a
// queued or running task, or a panel action neither delivered nor expired.
// A read error is logged and counts as busy, so nothing is deleted on a
// guess.
func (o *Orchestrator) panelBusy(ctx context.Context, convKey string) bool {
	if o.Busy(convKey) {
		return true
	}
	n, err := o.st.PendingPanelActions(ctx, convKey, o.now())
	if err != nil {
		o.log.Warn("load pending panel actions failed", "conversation", convKey, "error", err)
		return true
	}
	return n > 0
}

// WithdrawPanel deletes the messages of panel chat convKey from seq onward,
// with their search index, chunks and timers, and returns how many it
// deleted. It holds the chat's panel lock and, like a message of the user
// (see submit), keeps wake tasks from starting meanwhile (holdWakes). The
// chat's pending and running state steps are cancelled once the messages are
// deleted, their turns dropped: they may cover them. A busy chat is
// ErrChatNotIdle; a conversation not on store.PlatformPanel
// ErrNotPanelChat, one that is not private ErrNotPrivate, an unknown one
// store.ErrNotFound and a seq that is not a message of the chat
// ErrMessageNotFound; a bot being deleted is ErrBotDeleting.
func (o *Orchestrator) WithdrawPanel(ctx context.Context, convKey string, seq int64) (int, error) {
	botID, _, _ := strings.Cut(convKey, ":")
	if err := o.panelBots.notDeleting(botID); err != nil {
		return 0, err
	}
	unlock := o.panelLocks.lock(convKey)
	defer unlock()
	if err := o.panelBots.notDeleting(botID); err != nil {
		return 0, err
	}
	conv, err := o.panelConversation(ctx, convKey)
	if err != nil {
		return 0, err
	}
	if conv.Platform != store.PlatformPanel {
		return 0, ErrNotPanelChat
	}
	if conv.Type != conloomapi.ConvPrivate {
		return 0, ErrNotPrivate
	}
	defer o.holdWakes(convKey)()
	if o.panelBusy(ctx, convKey) {
		return 0, ErrChatNotIdle
	}
	found, err := o.st.MessagesBySeq(ctx, convKey, []int64{seq})
	if err != nil {
		return 0, err
	}
	target, ok := found[seq]
	if !ok {
		return 0, ErrMessageNotFound
	}
	actor := target.PlatformUserID
	if actor == "" {
		older, err := o.st.MessagesBefore(ctx, convKey, seq, 50)
		if err != nil {
			return 0, err
		}
		for _, m := range older {
			if !m.FromBot {
				actor = m.PlatformUserID
				break
			}
		}
	}
	if actor != "" {
		o.sched.EndTurn(convKey, actor)
	}
	deleted, err := o.st.DeleteMessagesFrom(ctx, convKey, seq)
	if err != nil {
		return 0, err
	}
	o.cancelStateSteps(convKey, "")
	o.suggestions.forget(convKey)
	o.replanInitiate(ctx, convKey)
	return deleted, nil
}

// regenScan bounds the messages of one reply that regenerating it reads.
const regenScan = 50

// regenTurn is what regenerating a panel chat's newest message redoes: task,
// the ordinary reply task whose reply it ends, and that task's trigger
// messages, oldest first.
type regenTurn struct {
	task     store.TaskRecord
	triggers []store.MessageRecord
}

// regenerable finds what regenerating the message seq of the panel chat conv
// would redo. It must be the chat's newest message and from the bot
// (ErrNotLatest; ErrMessageNotFound when it is not a message of the chat),
// and end the reply of an ordinary reply task to the user's messages: a task
// triggered by the user's private messages, not a wake, proactive or
// command task (ErrNotReply, ErrCommandReply), whose trigger messages are all
// still stored, none of them a command under the chat's policy, and after the
// last of which every message is a part of its reply (ErrNotReply). A reply to
// messages sent before their sender was forgotten is ErrForgotten.
func (o *Orchestrator) regenerable(ctx context.Context, conv store.ConversationRecord, seq int64) (regenTurn, error) {
	found, err := o.st.MessagesBySeq(ctx, conv.Key, []int64{seq})
	if err != nil {
		return regenTurn{}, err
	}
	target, ok := found[seq]
	if !ok {
		return regenTurn{}, ErrMessageNotFound
	}
	if !target.FromBot {
		return regenTurn{}, ErrNotLatest
	}
	after, err := o.st.MessagesAfter(ctx, conv.Key, seq, 1)
	if err != nil {
		return regenTurn{}, err
	}
	if len(after) != 0 {
		return regenTurn{}, ErrNotLatest
	}
	taskID, err := o.replyTaskID(ctx, target)
	if err != nil {
		return regenTurn{}, err
	}
	task, err := o.st.GetTask(ctx, conv.BotInstanceID, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return regenTurn{}, ErrNotReply
	}
	if err != nil {
		return regenTurn{}, err
	}
	if task.Command != "" || task.Meta.Trigger == conloomapi.TriggerCommand {
		return regenTurn{}, ErrCommandReply
	}
	if task.ConversationKey != conv.Key || task.Meta.Trigger != conloomapi.TriggerPrivate || len(task.TriggerMessageIDs) == 0 {
		return regenTurn{}, ErrNotReply
	}
	triggers, err := o.st.GetMessages(ctx, conv.BotInstanceID, task.TriggerMessageIDs)
	if err != nil {
		return regenTurn{}, err
	}
	if len(triggers) != len(task.TriggerMessageIDs) {
		return regenTurn{}, ErrNotReply
	}
	pv, err := o.policies.Current(ctx, conv.Key)
	if err != nil {
		return regenTurn{}, err
	}
	for _, m := range triggers {
		if m.ConversationKey != conv.Key || m.FromBot {
			return regenTurn{}, ErrNotReply
		}
		if _, _, ok := ParseCommand(m.Text, pv.Policy.Reply.CommandPrefix); ok {
			return regenTurn{}, ErrCommandReply
		}
	}
	slices.SortFunc(triggers, func(a, b store.MessageRecord) int { return cmp.Compare(a.Seq, b.Seq) })
	last := triggers[len(triggers)-1]
	reply, err := o.st.MessagesAfter(ctx, conv.Key, last.Seq, regenScan)
	if err != nil {
		return regenTurn{}, err
	}
	if len(reply) == 0 || reply[len(reply)-1].Seq != seq {
		return regenTurn{}, ErrNotReply
	}
	for _, m := range reply[:len(reply)-1] {
		if !m.FromBot {
			return regenTurn{}, ErrNotReply
		}
		id, err := o.replyTaskID(ctx, m)
		if err != nil {
			return regenTurn{}, err
		}
		if id != task.ID {
			return regenTurn{}, ErrNotReply
		}
	}
	rows, err := o.st.GetMemberMemories(ctx, memory.ScopeKeyOf(conv.Key), []string{task.ActorPlatformUserID})
	if err != nil {
		return regenTurn{}, err
	}
	if forgotten := rows[task.ActorPlatformUserID].ForgottenAt; !forgotten.IsZero() && last.CreatedAt.Before(forgotten) {
		return regenTurn{}, ErrForgotten
	}
	return regenTurn{task: task, triggers: triggers}, nil
}

// replyTaskID returns the ID of the task whose panel action delivered the bot
// message m (stored with the platform message ID panel_<action ID>, see
// outbox.PanelDeliverer); ErrNotReply when no panel action did.
func (o *Orchestrator) replyTaskID(ctx context.Context, m store.MessageRecord) (string, error) {
	actionID, ok := strings.CutPrefix(m.PlatformMessageID, "panel_")
	if !ok {
		return "", ErrNotReply
	}
	rec, err := o.st.GetOutbox(ctx, m.BotInstanceID, actionID)
	if errors.Is(err, store.ErrNotFound) || err == nil && rec.ConversationKey != m.ConversationKey {
		return "", ErrNotReply
	}
	if err != nil {
		return "", err
	}
	return rec.TaskID, nil
}

// regenRefused reports whether err is regenerable's refusal of a message
// rather than a failure.
func regenRefused(err error) bool {
	for _, refusal := range []error{ErrNotLatest, ErrNotReply, ErrCommandReply, ErrForgotten, store.ErrNotFound} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// RegenerableSeq returns the seq of the panel chat's newest message when
// RegeneratePanel would accept it now, and 0 when it would refuse it: the
// chat is busy or paused, its bot is being deleted, or the message is not the
// bot's ordinary reply to the user's messages before it (see regenerable).
// A conversation that is not a private panel chat has none; an unknown one
// is store.ErrNotFound.
func (o *Orchestrator) RegenerableSeq(ctx context.Context, convKey string) (int64, error) {
	conv, err := o.panelConversation(ctx, convKey)
	if err != nil {
		return 0, err
	}
	if conv.Platform != store.PlatformPanel || conv.Type != conloomapi.ConvPrivate {
		return 0, nil
	}
	if _, ok := o.bots.Bot(conv.BotInstanceID); !ok || o.panelBots.notDeleting(conv.BotInstanceID) != nil || o.panelBusy(ctx, convKey) {
		return 0, nil
	}
	newest, err := o.newestSeq(ctx, convKey)
	if err != nil || newest == 0 {
		return 0, err
	}
	if _, err := o.regenerable(ctx, conv, newest); err != nil {
		if regenRefused(err) {
			return 0, nil
		}
		return 0, err
	}
	pv, err := o.policies.Current(ctx, convKey)
	if err != nil {
		return 0, err
	}
	if pv.Policy.Concurrency.MaxInteractiveRequests == 0 {
		return 0, nil
	}
	return newest, nil
}

// RegeneratePanel deletes the newest bot reply of panel chat convKey and
// queues a new reply task for the same user messages, without storing them
// again: the new task's trigger messages are those of the reply's task, and
// everything after the last of them is deleted in the same transaction that
// stores the task; the discarded reply's state step turn is then dropped
// (abandonStateTurn), so no state step judges it. Only the bot's ordinary
// reply to the user's messages can be regenerated (see regenerable:
// ErrNotLatest, ErrNotReply, ErrCommandReply, ErrForgotten,
// ErrMessageNotFound). It holds the chat's panel lock and keeps
// wake tasks from starting meanwhile (holdWakes). A busy chat is
// ErrChatNotIdle, a paused one errPaused (ErrPaused), a full queue
// ErrQueueFull and a task of the same sender created meanwhile
// ErrRegenMerged; a conversation not on store.PlatformPanel is
// ErrNotPanelChat, one that is not private ErrNotPrivate, an unknown one or
// one of an unknown bot store.ErrNotFound, and a bot being deleted
// ErrBotDeleting.
func (o *Orchestrator) RegeneratePanel(ctx context.Context, convKey string, seq int64) (string, error) {
	botID, _, _ := strings.Cut(convKey, ":")
	if err := o.panelBots.notDeleting(botID); err != nil {
		return "", err
	}
	unlock := o.panelLocks.lock(convKey)
	defer unlock()
	if err := o.panelBots.notDeleting(botID); err != nil {
		return "", err
	}
	conv, err := o.panelConversation(ctx, convKey)
	if err != nil {
		return "", err
	}
	if conv.Platform != store.PlatformPanel {
		return "", ErrNotPanelChat
	}
	if conv.Type != conloomapi.ConvPrivate {
		return "", ErrNotPrivate
	}
	if _, ok := o.bots.Bot(conv.BotInstanceID); !ok {
		return "", fmt.Errorf("%w: unknown bot %q", store.ErrNotFound, conv.BotInstanceID)
	}
	defer o.holdWakes(convKey)()
	if o.panelBusy(ctx, convKey) {
		return "", ErrChatNotIdle
	}
	turn, err := o.regenerable(ctx, conv, seq)
	if err != nil {
		return "", err
	}
	actor := turn.task.ActorPlatformUserID
	last := turn.triggers[len(turn.triggers)-1]
	o.sched.EndTurn(convKey, actor)
	closed := MergeOptions{Debounce: time.Nanosecond, MaxMergeWindow: time.Nanosecond}
	now := o.now()
	var taskID string
	_, err = o.sched.AdmitWith(convKey, actor, last.ID, AdmitNew, closed, func(ap AdmitPlan) error {
		if ap.NewTaskID == "" {
			return ErrRegenMerged
		}
		if ap.Supersedes != "" {
			return ErrSupersedeRefused
		}
		pv, err := o.policies.Current(ctx, convKey)
		if err != nil {
			return err
		}
		if pv.Policy.Concurrency.MaxInteractiveRequests == 0 {
			return errPaused
		}
		id := ap.NewTaskID
		task := store.TaskRecord{
			ID: id, BotInstanceID: conv.BotInstanceID, ConversationKey: convKey, Status: conloomapi.TaskQueued,
			ActorPlatformUserID: actor, TriggerMessageIDs: slices.Clone(turn.task.TriggerMessageIDs),
			Priority: int(admission.PriorityInteractive), Meta: conloomapi.TaskMeta{Trigger: conloomapi.TriggerPrivate},
			EventOccurredAt: now.UTC(), CreatedAt: now, UpdatedAt: now,
		}
		resp, err := json.Marshal(conloomapi.EventAccepted{
			Disposition: conloomapi.DispositionReply, TaskID: id, Status: conloomapi.TaskQueued,
		})
		if err != nil {
			return err
		}
		ev := store.EventRecord{
			BotInstanceID: conv.BotInstanceID, IdempotencyKey: "conloom:regen:" + id, EventID: "regen_" + id,
			Response: resp, CreatedAt: now,
		}
		if err := o.ingest(ctx, store.Ingest{
			Event: ev, NewTask: &task,
			DeleteMessages: &store.MessageDelete{ConversationKey: convKey, FromSeq: last.Seq + 1},
		}); err != nil {
			return err
		}
		taskID = id
		o.abandonStateTurn(turn.task.ID)
		return nil
	})
	if err != nil {
		return "", err
	}
	if taskID == "" {
		return "", ErrRegenMerged
	}
	o.suggestions.forget(convKey)
	o.replanInitiate(ctx, convKey)
	return taskID, nil
}

// awaitStopped waits up to panelDeleteWait in all for the cancelled tasks'
// owners to let go of them, and returns ErrChatBusy if they did not.
func (o *Orchestrator) awaitStopped(ctx context.Context, convKey string, owners []<-chan struct{}) error {
	if len(owners) == 0 {
		return nil
	}
	timer := time.NewTimer(o.panelDeleteWait)
	defer timer.Stop()
	for _, done := range owners {
		select {
		case <-done:
		case <-timer.C:
			o.log.Warn("panel chat not deleted: its cancelled tasks are still stopping", "conversation", convKey)
			return ErrChatBusy
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Busy reports whether the conversation has queued or running tasks.
func (o *Orchestrator) Busy(convKey string) bool {
	return o.sched.Busy(convKey)
}

// panelConversation loads the conversation of convKey; a malformed key, or
// one naming a stored conversation by another key, is store.ErrNotFound.
func (o *Orchestrator) panelConversation(ctx context.Context, convKey string) (store.ConversationRecord, error) {
	parts := strings.SplitN(convKey, ":", 4)
	if len(parts) != 4 {
		return store.ConversationRecord{}, store.ErrNotFound
	}
	conv, err := o.st.GetConversation(ctx, parts[0], parts[3])
	if err != nil {
		return store.ConversationRecord{}, err
	}
	if conv.Key != convKey {
		return store.ConversationRecord{}, store.ErrNotFound
	}
	return conv, nil
}

// PanelActorID is the actor and platform user ID of panelUser in panel chats:
// panel_<panelUser>, with any ':' (which separates the parts of keys built
// from it) replaced by '_'.
func PanelActorID(panelUser string) string {
	return "panel_" + strings.ReplaceAll(panelUser, ":", "_")
}

// panelChatName is displayName trimmed, or panelUser when that is empty, cut
// to panelNameMax characters.
func panelChatName(displayName, panelUser string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = panelUser
	}
	if r := []rune(name); len(r) > panelNameMax {
		name = string(r[:panelNameMax])
	}
	return name
}

// convPlatform returns the platform part of a "bot:platform:type:id"
// conversation key, or "" when the key is malformed.
func convPlatform(convKey string) string {
	parts := strings.SplitN(convKey, ":", 4)
	if len(parts) != 4 {
		return ""
	}
	return parts[1]
}

// lockTable serialises work per key with a mutex for each key in use, so
// work on one key never waits for another; a key's entry is dropped once no
// one holds or waits for it. The zero value is ready to use.
type lockTable struct {
	mu    sync.Mutex
	locks map[string]*tableLock
}

type tableLock struct {
	mu sync.Mutex
	// users counts who hold or wait for mu, guarded by lockTable.mu.
	users int
}

func (l *lockTable) lock(key string) (unlock func()) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*tableLock{}
	}
	e := l.locks[key]
	if e == nil {
		e = &tableLock{}
		l.locks[key] = e
	}
	e.users++
	l.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		if e.users--; e.users == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}
