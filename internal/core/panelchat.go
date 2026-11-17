package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// panelTextMax is the most characters of a panel message.
	panelTextMax = 2000
	// panelNameMax is the most characters of a panel chat's display name.
	panelNameMax = 32
)

// SubmitPanel accepts text written in the panel by panelUser into the panel
// chat convKey (store.PlatformPanel), like Submit accepts an event: the
// message is stored on the panel channel and the bot's replies there are
// delivered to the panel only (see replyChannel). It speaks as
// panel_<panelUser>, under the display name the chat was created with, and
// returns the new message's ID and, when the message is stored but will not
// be answered, why (conloomapi.EventAccepted.SuppressedReason, such as
// paused, rate_limited or queue_full). A conversation on another platform is
// ErrNotPanelChat; an unknown conversation, also one deleted meanwhile (see
// DeletePanelChat), store.ErrNotFound; one whose bot is being deleted (see
// PanelBotService.Delete) ErrBotDeleting.
func (o *Orchestrator) SubmitPanel(ctx context.Context, convKey, panelUser, text string) (messageID, suppressed string, err error) {
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
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > panelTextMax {
		return "", "", ErrBadText
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
			Mentions: []conloomapi.Mention{}, Attachments: []conloomapi.Attachment{}},
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return "", "", err
	}
	resp, _, err := o.submitOn(ctx, conv.BotInstanceID, pmid, body, ev, store.ChannelPanel)
	if err != nil {
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
// to stop, drops its scheduler session, deletes what is stored under convKey
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
// with their search index, chunks and timers. It holds the chat's panel lock,
// refuses a busy chat with ErrChatNotIdle, and returns how many messages it
// deleted. A conversation not on store.PlatformPanel is ErrNotPanelChat, an
// unknown one or one without the message store.ErrNotFound.
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
	if o.panelBusy(ctx, convKey) {
		return 0, ErrChatNotIdle
	}
	found, err := o.st.MessagesBySeq(ctx, convKey, []int64{seq})
	if err != nil {
		return 0, err
	}
	target, ok := found[seq]
	if !ok {
		return 0, store.ErrNotFound
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
	o.suggestions.forget(convKey)
	o.replanInitiate(ctx, convKey)
	return deleted, nil
}

// RegeneratePanel deletes the newest bot reply of panel chat convKey and
// queues a new reply task for the user message before it, without storing a
// duplicate user message. It holds the chat's panel lock and refuses a busy
// chat with ErrChatNotIdle. The target must be the chat's newest message and
// from the bot, or it is ErrNotLatest. The old reply's messages, chunks and
// timers go in the same transaction that stores the task.
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
	if o.panelBusy(ctx, convKey) {
		return "", ErrChatNotIdle
	}
	found, err := o.st.MessagesBySeq(ctx, convKey, []int64{seq})
	if err != nil {
		return "", err
	}
	target, ok := found[seq]
	if !ok {
		return "", store.ErrNotFound
	}
	if !target.FromBot {
		return "", ErrNotLatest
	}
	after, err := o.st.MessagesAfter(ctx, convKey, seq, 1)
	if err != nil {
		return "", err
	}
	if len(after) != 0 {
		return "", ErrNotLatest
	}
	older, err := o.st.MessagesBefore(ctx, convKey, seq, 50)
	if err != nil {
		return "", err
	}
	var user store.MessageRecord
	for _, m := range older {
		if !m.FromBot {
			user = m
			break
		}
	}
	if user.ID == "" {
		return "", ErrNotLatest
	}
	o.sched.EndTurn(convKey, user.PlatformUserID)
	closed := MergeOptions{Debounce: time.Nanosecond, MaxMergeWindow: time.Nanosecond}
	now := o.now()
	var taskID string
	_, err = o.sched.AdmitWith(convKey, user.PlatformUserID, user.ID, AdmitNew, closed, func(ap AdmitPlan) error {
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
		if _, _, ok := ParseCommand(user.Text, pv.Policy.Reply.CommandPrefix); ok {
			return ErrCommandReply
		}
		id := ap.NewTaskID
		task := store.TaskRecord{
			ID: id, BotInstanceID: target.BotInstanceID, ConversationKey: convKey, Status: conloomapi.TaskQueued,
			ActorPlatformUserID: user.PlatformUserID, TriggerMessageIDs: []string{user.ID},
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
			BotInstanceID: target.BotInstanceID, IdempotencyKey: "conloom:regen:" + id, EventID: "regen_" + id,
			Response: resp, CreatedAt: now,
		}
		if err := o.ingest(ctx, store.Ingest{
			Event: ev, NewTask: &task,
			DeleteMessages: &store.MessageDelete{ConversationKey: convKey, FromSeq: user.Seq + 1},
		}); err != nil {
			return err
		}
		taskID = id
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
