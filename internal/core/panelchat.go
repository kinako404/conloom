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

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// panelTextMax is the most characters of a panel message.
	panelTextMax = 2000
	// panelNameMax is the most characters of a panel chat's display name.
	panelNameMax = 32
	// panelSpeakerLookback is how many of a conversation's newest messages
	// SubmitPanel searches for the user it speaks as.
	panelSpeakerLookback = 200
	// platformActorPrefix starts the actor ID the adapters give a platform
	// user: user_<platform user ID>.
	platformActorPrefix = "user_"
)

// SubmitPanel accepts text written in the panel by panelUser into the private
// conversation convKey, like Submit accepts an event: the message is stored
// on the panel channel, so the reply to a task it is the newest trigger of is
// delivered to the panel only (see replyChannel). In a panel chat
// (store.PlatformPanel) it speaks as panel_<panelUser>, under the display name
// the chat was created with; elsewhere it takes over the conversation's user
// (see panelSpeaker). It returns the new message's ID. A message the bot
// ignores, and so does not store (its user is not allowed to chat privately),
// is ErrNotAllowed; an unknown conversation, also one deleted meanwhile (see
// DeletePanelChat), store.ErrNotFound.
func (o *Orchestrator) SubmitPanel(ctx context.Context, convKey, panelUser, text string) (messageID string, err error) {
	unlock := o.panelLocks.lock(convKey)
	defer unlock()
	conv, err := o.panelConversation(ctx, convKey)
	if err != nil {
		return "", err
	}
	if conv.Type != conloomapi.ConvPrivate {
		return "", ErrNotPrivate
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > panelTextMax {
		return "", ErrBadText
	}
	bot, ok := o.cfg.Bot(conv.BotInstanceID)
	if !ok {
		return "", fmt.Errorf("%w: bot %q is not configured", store.ErrNotFound, conv.BotInstanceID)
	}
	actor, err := o.panelSpeaker(ctx, conv, bot, panelUser)
	if err != nil {
		return "", err
	}
	pmid := ids.New("panel")
	ev := conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion, EventID: ids.New("evt"), EventType: conloomapi.EventTypeMessageCreated,
		BotInstanceID: conv.BotInstanceID, Platform: conv.Platform, OccurredAt: o.now().UTC(),
		Conversation: conloomapi.Conversation{Type: conv.Type, ID: conv.ID, PlatformConversationID: conv.PlatformConversationID},
		Actor:        actor,
		Trigger:      conloomapi.Trigger{Type: conloomapi.TriggerPrivate, RequestedResponse: true},
		Message: conloomapi.Message{ID: "msg_" + pmid, PlatformMessageID: pmid, Text: text,
			Mentions: []conloomapi.Mention{}, Attachments: []conloomapi.Attachment{}},
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	resp, _, err := o.submitOn(ctx, conv.BotInstanceID, pmid, body, ev, store.ChannelPanel)
	if err != nil {
		return "", err
	}
	// Every disposition but ignore stores the message.
	if resp.Disposition == conloomapi.DispositionIgnore {
		return "", ErrNotAllowed
	}
	return ev.Message.ID, nil
}

// panelSpeaker is the actor a panel message in conv is sent as (see
// SubmitPanel). Outside panel chats it is the sender of the newest user
// message among the conversation's newest panelSpeakerLookback messages or,
// without one there, the user the private conversation is with (its
// PlatformConversationID) if they ever wrote in it, under their newest
// display name. It is never the bot itself; a conversation without a known
// user is ErrNoUserYet.
func (o *Orchestrator) panelSpeaker(ctx context.Context, conv store.ConversationRecord, bot config.BotConfig, panelUser string) (conloomapi.Actor, error) {
	if conv.Platform == store.PlatformPanel {
		id := panelActorID(panelUser)
		return conloomapi.Actor{ID: id, PlatformUserID: id, DisplayName: o.panelDisplayName(ctx, conv, id, panelUser)}, nil
	}
	recent, err := o.st.RecentMessages(ctx, conv.Key, panelSpeakerLookback)
	if err != nil {
		return conloomapi.Actor{}, err
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if m := recent[i]; !m.FromBot && m.PlatformUserID != "" && m.PlatformUserID != bot.PlatformSelfID {
			return conloomapi.Actor{ID: m.ActorID, PlatformUserID: m.PlatformUserID, DisplayName: m.DisplayName}, nil
		}
	}
	uid := conv.PlatformConversationID
	if uid == "" || uid == bot.PlatformSelfID {
		return conloomapi.Actor{}, ErrNoUserYet
	}
	actorID := platformActorPrefix + uid
	users, err := o.st.UserIDsForActors(ctx, conv.Key, []string{actorID})
	if err != nil {
		return conloomapi.Actor{}, err
	}
	if users[actorID] == "" {
		return conloomapi.Actor{}, ErrNoUserYet
	}
	names, err := o.st.LatestDisplayNames(ctx, conv.Key, []string{actorID})
	if err != nil {
		return conloomapi.Actor{}, err
	}
	return conloomapi.Actor{ID: actorID, PlatformUserID: users[actorID], DisplayName: names[actorID]}, nil
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
// bot is store.ErrNotFound.
func (o *Orchestrator) CreatePanelChat(ctx context.Context, botID, panelUser, displayName string) (convKey string, err error) {
	if _, ok := o.cfg.Bot(botID); !ok {
		return "", fmt.Errorf("%w: bot %q is not configured", store.ErrNotFound, botID)
	}
	convID := ids.New("panel")
	convKey = store.ConversationKey(botID, store.PlatformPanel, conloomapi.ConvPrivate, convID)
	actorID := panelActorID(panelUser)
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
// to stop, drops its scheduler session, deletes what is stored under
// convKey (see store.Store.DeleteConversation) and then forgets the chat's
// session, lull timer, rate-limit hits and cached persona prompt and policy.
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
	o.log.Info("panel chat deleted", "conversation", convKey)
	return nil
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

// panelActorID is the actor and platform user ID of panelUser in panel chats:
// panel_<panelUser>, with any ':' (which separates the parts of keys built
// from it) replaced by '_'.
func panelActorID(panelUser string) string {
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
