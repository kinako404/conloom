package core

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/outbox"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func privateKey(user string) string {
	return store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "private_"+user)
}

func allowAllPrivate(c *config.Config) { c.Bots[0].PrivateChat = "all" }

// privateWait makes private turns wait d for the sender to pause.
func privateWait(d time.Duration) func(*config.Config) {
	return func(c *config.Config) {
		c.Bots[0].PrivateChat = "all"
		c.DefaultGroupPolicy.Reply.PrivateWait = conloomapi.Duration{Duration: d}
		c.DefaultGroupPolicy.Reply.PrivateMaxWait = conloomapi.Duration{Duration: 10 * d}
	}
}

func (e *testEnv) submitPanel(t *testing.T, convKey, panelUser, text string) string {
	t.Helper()
	id, _, err := e.o.SubmitPanel(context.Background(), convKey, panelUser, text)
	if err != nil {
		t.Fatalf("SubmitPanel(%q): %v", text, err)
	}
	return id
}

// panelResponse returns how the panel message messageID was accepted.
func (e *testEnv) panelResponse(t *testing.T, messageID string) conloomapi.EventAccepted {
	t.Helper()
	rec, err := e.st.GetEvent(context.Background(), testBot, strings.TrimPrefix(messageID, "msg_"))
	if err != nil {
		t.Fatalf("event of %s: %v", messageID, err)
	}
	var resp conloomapi.EventAccepted
	if err := json.Unmarshal(rec.Response, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// actionChannels returns the channels of the task's actions.
func (e *testEnv) actionChannels(t *testing.T, task conloomapi.Task) []string {
	t.Helper()
	var out []string
	for _, a := range task.Actions {
		rec, err := e.st.GetOutbox(context.Background(), testBot, a.ActionID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rec.Channel)
	}
	return out
}

func wantChannels(t *testing.T, what string, got []string, want string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("%s: no actions", what)
	}
	for _, c := range got {
		if c != want {
			t.Fatalf("%s: action channels %q, want all %q", what, got, want)
		}
	}
}

func TestSubmitPanel_RepliesOnPanelOnly(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()

	first := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗")).TaskID)
	wantChannels(t, "platform message", env.actionChannels(t, first), "")
	platformAction := first.Actions[0].ActionID

	key := env.createChat(t, testBot)
	id := env.submitPanel(t, key, "admin", "今天过得怎么样")
	msg, err := env.st.GetMessage(ctx, testBot, id)
	if err != nil {
		t.Fatalf("panel message not stored: %v", err)
	}
	if msg.Channel != store.ChannelPanel || msg.PlatformUserID != "panel_admin" || msg.ActorID != "panel_admin" ||
		msg.DisplayName != "小明" || msg.Platform != store.PlatformPanel || msg.ConversationKey != key || msg.FromBot {
		t.Fatalf("panel message = %+v", msg)
	}
	if !strings.HasPrefix(msg.PlatformMessageID, "panel_") || strings.Contains(msg.PlatformMessageID, ":") {
		t.Errorf("platform message ID = %q, want panel_<random>", msg.PlatformMessageID)
	}
	resp := env.panelResponse(t, id)
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("panel message response = %+v", resp)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	wantChannels(t, "panel message", env.actionChannels(t, task), store.ChannelPanel)
	panelAction := task.Actions[0].ActionID

	leased, err := env.st.LeaseOutbox(ctx, testBot, 10, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range leased {
		if r.Action.ActionID == panelAction {
			t.Fatalf("adapter leased the panel reply %s", panelAction)
		}
	}

	svc := outbox.New(env.st, outbox.Config{}, nil, nil)
	d := outbox.NewPanelDeliverer(svc, env.st, time.Hour, nil, nil)
	d.Start()
	defer d.Close()
	d.Wake()
	waitUntil(t, "panel reply acked", func() bool {
		rec, err := env.st.GetOutbox(ctx, testBot, panelAction)
		return err == nil && rec.Status == store.OutboxAcked
	})
	bot, err := env.st.GetMessage(ctx, testBot, "msg_panel_"+panelAction)
	if err != nil {
		t.Fatalf("bot message of the panel reply: %v", err)
	}
	if !bot.FromBot || bot.Channel != store.ChannelPanel || bot.PlatformMessageID != "panel_"+panelAction {
		t.Errorf("bot message = %+v", bot)
	}
	if rec, err := env.st.GetOutbox(ctx, testBot, platformAction); err != nil || rec.Status == store.OutboxAcked {
		t.Errorf("platform reply = %+v, %v; the panel deliverer must leave it to the adapter", rec, err)
	}
}

func TestSubmitPanel_RateLimited(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 1
	}})
	key := env.createChat(t, testBot)
	ctx := context.Background()
	first, suppressed, err := env.o.SubmitPanel(ctx, key, "admin", "在吗")
	if err != nil || suppressed != "" {
		t.Fatalf("first SubmitPanel = %q, suppressed %q, %v; want an answered message", first, suppressed, err)
	}
	env.waitTask(t, env.panelResponse(t, first).TaskID)
	id, suppressed, err := env.o.SubmitPanel(ctx, key, "admin", "还在吗")
	if err != nil {
		t.Fatal(err)
	}
	resp := env.panelResponse(t, id)
	if resp.Disposition != conloomapi.DispositionObserve || resp.SuppressedReason != conloomapi.SuppressedRateLimited {
		t.Fatalf("panel message response = %+v, want observed as rate limited", resp)
	}
	if suppressed != conloomapi.SuppressedRateLimited {
		t.Errorf("SubmitPanel suppressed = %q, want %q", suppressed, conloomapi.SuppressedRateLimited)
	}
}

func TestSubmitPanel_Errors(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ctx := context.Background()
	key := env.createChat(t, testBot)

	for _, c := range []struct {
		name, key, text string
		want            error
	}{
		{"unknown conversation", privateKey("20009"), "你好", store.ErrNotFound},
		{"unknown panel chat", store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_missing"), "你好",
			store.ErrNotFound},
		{"malformed key", "bot_main:qq", "你好", store.ErrNotFound},
		{"empty", key, "", ErrBadText},
		{"blank", key, " \n ", ErrBadText},
		{"too long", key, strings.Repeat("长", 2001), ErrBadText},
	} {
		if _, _, err := env.o.SubmitPanel(ctx, c.key, "admin", c.text); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, _, err := env.o.SubmitPanel(ctx, key, "admin", strings.Repeat("长", 2000)); err != nil {
		t.Errorf("2000 characters: %v", err)
	}
}

func TestSubmitPanel_RejectsPlatform(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	private := privateKey("20001")
	env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗")).TaskID)
	if _, err := env.st.Ingest(ctx, store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed_group", EventID: "evt_seed_group"},
		Conversation: &store.ConversationRecord{BotInstanceID: testBot, ID: "group_10001", Key: testConvKey,
			Type: conloomapi.ConvGroup, Platform: "qq", PlatformConversationID: "10001"},
	}); err != nil {
		t.Fatal(err)
	}
	stored := func() map[string]int {
		rows := rowsOfBot(t, env.dbPath, testBot)
		return map[string]int{"events": rows["events"], "messages": rows["messages"], "tasks": rows["tasks"],
			"outbox_actions": rows["outbox_actions"]}
	}
	before := stored()
	for _, key := range []string{private, testConvKey} {
		if _, _, err := env.o.SubmitPanel(ctx, key, "admin", "你好"); !errors.Is(err, ErrNotPanelChat) {
			t.Errorf("%s: err = %v, want ErrNotPanelChat", key, err)
		}
	}
	if after := stored(); !maps.Equal(after, before) {
		t.Errorf("rows after the refused messages = %v, want %v", after, before)
	}
	if env.o.Busy(private) {
		t.Error("the platform chat is busy after a refused panel message")
	}
}

func TestPanelChat_AdminWithoutPrivateChatAllowed(t *testing.T) {
	for _, mode := range []string{"off", "admins", "allowlist"} {
		t.Run(mode, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
				c.Bots[0].PrivateChat = mode
				c.Bots[0].PlatformSelfID = "panel_admin"
			}})
			ctx := context.Background()
			key, err := env.o.CreatePanelChat(ctx, testBot, "admin", "  小明 ")
			if err != nil {
				t.Fatalf("CreatePanelChat: %v", err)
			}
			id := env.submitPanel(t, key, "admin", "你好")
			msg, err := env.st.GetMessage(ctx, testBot, id)
			if err != nil {
				t.Fatal(err)
			}
			if msg.ActorID != "panel_admin" || msg.PlatformUserID != "panel_admin" || msg.DisplayName != "小明" ||
				msg.Channel != store.ChannelPanel || msg.Platform != store.PlatformPanel {
				t.Fatalf("panel chat message = %+v", msg)
			}
			resp := env.panelResponse(t, id)
			if resp.Disposition != conloomapi.DispositionReply {
				t.Fatalf("response = %+v", resp)
			}
			task := env.waitTask(t, resp.TaskID)
			if task.Status != conloomapi.TaskSucceeded || task.Actions[0].Content.Text != fakeReply {
				t.Fatalf("task = %+v", task)
			}
			wantChannels(t, "panel chat", env.actionChannels(t, task), store.ChannelPanel)

			cmd := env.panelResponse(t, env.submitPanel(t, key, "admin", "/pause"))
			if task := env.getTask(t, cmd.TaskID); len(task.Actions) != 1 || task.Actions[0].Content.Text != CopyPaused {
				t.Fatalf("/pause = %+v, want %q", task, CopyPaused)
			}
		})
	}
}

func TestCreatePanelChat(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ctx := context.Background()
	if _, err := env.o.CreatePanelChat(ctx, "bot_missing", "admin", "小明"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown bot: err = %v, want store.ErrNotFound", err)
	}
	for _, c := range []struct{ name, want string }{
		{"   ", "admin"},
		{strings.Repeat("名", 40), strings.Repeat("名", 32)},
	} {
		key, err := env.o.CreatePanelChat(ctx, testBot, "admin", c.name)
		if err != nil {
			t.Fatalf("CreatePanelChat(%q): %v", c.name, err)
		}
		parts := strings.Split(key, ":")
		if len(parts) != 4 || parts[0] != testBot || parts[1] != store.PlatformPanel || parts[2] != conloomapi.ConvPrivate ||
			!strings.HasPrefix(parts[3], "panel_") {
			t.Fatalf("key = %q, want %s:panel:private:panel_<random>", key, testBot)
		}
		conv, err := env.st.GetConversation(ctx, testBot, parts[3])
		if err != nil || conv.Key != key || conv.Platform != store.PlatformPanel {
			t.Fatalf("conversation = %+v, %v", conv, err)
		}
		msg, err := env.st.GetMessage(ctx, testBot, env.submitPanel(t, key, "admin", "你好"))
		if err != nil || msg.DisplayName != c.want {
			t.Errorf("display name for %q = %q (%v), want %q", c.name, msg.DisplayName, err, c.want)
		}
	}
}

func TestNewActions_Channel(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ctx := context.Background()
	task := store.TaskRecord{ID: "task_1", BotInstanceID: testBot, ConversationKey: privateKey("20001")}
	platform := store.ConversationRecord{BotInstanceID: testBot, ID: "private_20001", Type: conloomapi.ConvPrivate, Platform: "qq"}
	panel := platform
	panel.Platform = store.PlatformPanel
	fromPanel := store.MessageRecord{ID: "msg_1", Channel: store.ChannelPanel}
	fromPlatform := store.MessageRecord{ID: "msg_2"}
	for _, c := range []struct {
		name     string
		conv     store.ConversationRecord
		triggers []store.MessageRecord
		want     string
	}{
		{"no triggers on a platform", platform, nil, ""},
		{"no triggers in a panel chat", panel, nil, store.ChannelPanel},
		{"panel chat", panel, []store.MessageRecord{fromPanel}, store.ChannelPanel},
		{"platform", platform, []store.MessageRecord{fromPlatform}, ""},
		{"platform, newest written in the panel before", platform, []store.MessageRecord{fromPlatform, fromPanel}, ""},
	} {
		for _, parts := range [][]string{{"一"}, {"一", "二", "三"}} {
			for i, a := range env.o.newActions(ctx, task, c.conv, c.triggers, parts) {
				if a.Channel != c.want {
					t.Errorf("%s, %d parts: part %d channel = %q, want %q", c.name, len(parts), i+1, a.Channel, c.want)
				}
			}
		}
	}
}

func TestDeletePanelChat(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privateWait(time.Minute)})
	ctx := context.Background()
	env.submit(t, env.groupEvent("20001", "大家好"))
	key, err := env.o.CreatePanelChat(ctx, testBot, "admin", "小明")
	if err != nil {
		t.Fatal(err)
	}
	id := env.submitPanel(t, key, "admin", "你好")
	taskID := env.panelResponse(t, id).TaskID
	if !env.o.Busy(key) {
		t.Fatal("Busy = false with a queued task")
	}
	convID := strings.Split(key, ":")[3]
	if _, err := env.st.GetEvent(ctx, testBot, convID); err != nil {
		t.Fatalf("creation event: %v", err)
	}

	if err := env.o.DeletePanelChat(ctx, key); err != nil {
		t.Fatalf("DeletePanelChat: %v", err)
	}
	if env.o.Busy(key) {
		t.Error("Busy after delete")
	}
	env.o.sched.mu.Lock()
	_, scheduled := env.o.sched.sessions[key]
	env.o.sched.mu.Unlock()
	if scheduled {
		t.Error("scheduler still holds the conversation")
	}
	env.o.limiter.mu.Lock()
	for k := range env.o.limiter.hits {
		if strings.HasPrefix(k, key+":") {
			t.Errorf("rate limiter still holds %q", k)
		}
	}
	env.o.limiter.mu.Unlock()
	if _, err := env.st.GetConversation(ctx, testBot, strings.Split(key, ":")[3]); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("conversation after delete: %v", err)
	}
	if _, err := env.st.GetMessage(ctx, testBot, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("message after delete: %v", err)
	}
	if _, err := env.st.GetTask(ctx, testBot, taskID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("task after delete: %v", err)
	}
	if _, err := env.st.GetEvent(ctx, testBot, convID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("creation event after delete: %v", err)
	}
	if msgs, err := env.st.RecentMessages(ctx, testConvKey, 10); err != nil || len(msgs) != 1 {
		t.Errorf("other conversation's messages = %d, %v; want kept", len(msgs), err)
	}

	if err := env.o.DeletePanelChat(ctx, testConvKey); !errors.Is(err, ErrNotPanelChat) {
		t.Errorf("delete a platform conversation: err = %v, want ErrNotPanelChat", err)
	}
	if err := env.o.DeletePanelChat(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete twice: err = %v, want store.ErrNotFound", err)
	}
	if err := env.o.DeletePanelChat(ctx, "bot_main:panel"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("malformed key: err = %v, want store.ErrNotFound", err)
	}
}

func TestDeletePanelChat_CancelsRunningTask(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) {
		started <- struct{}{}
		<-release
		return 200, fakeReply
	}})
	t.Cleanup(func() { close(release) })
	ctx := context.Background()
	key, err := env.o.CreatePanelChat(ctx, testBot, "admin", "小明")
	if err != nil {
		t.Fatal(err)
	}
	taskID := env.panelResponse(t, env.submitPanel(t, key, "admin", "你好")).TaskID
	select {
	case <-started:
	case <-time.After(waitLimit):
		t.Fatal("model never called")
	}
	if err := env.o.DeletePanelChat(ctx, key); err != nil {
		t.Fatalf("DeletePanelChat: %v", err)
	}
	if env.o.Busy(key) {
		t.Error("Busy after delete")
	}
	if got := env.hooks.completed(taskID); len(got) != 1 || got[0] != conloomapi.TaskCancelled {
		t.Errorf("running task completed as %v, want cancelled before the delete", got)
	}
	if _, err := env.st.GetTask(ctx, testBot, taskID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("task after delete: %v", err)
	}
}

func TestSubmitPanel_InputReviewed(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	id := env.submitPanel(t, env.createChat(t, testBot), "admin", "你好\x07呀")
	if task := env.waitTask(t, env.panelResponse(t, id).TaskID); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	reqs := append(env.short.Requests(), env.long.Requests()...)
	last := requestMessages(t, reqs[len(reqs)-1])
	content, _ := last[len(last)-1]["content"].(string)
	if !strings.Contains(content, "你好呀") || strings.Contains(content, "\x07") {
		t.Errorf("model saw %q, want the reviewed text without control characters", content)
	}
}

func TestSubmitPanel_SupersedesRunningTask(t *testing.T) {
	var calls atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	model := func(map[string]any) (int, string) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-release
		}
		return 200, fakeReply
	}
	env := newTestOrchestrator(t, testOpts{short: model, long: model})
	t.Cleanup(func() { close(release) })
	key := env.createChat(t, testBot)
	first := env.panelResponse(t, env.submitPanel(t, key, "admin", "在吗"))
	select {
	case <-started:
	case <-time.After(waitLimit):
		t.Fatal("model never called")
	}
	resp := env.panelResponse(t, env.submitPanel(t, key, "admin", "换个话题"))
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == first.TaskID {
		t.Fatalf("panel message response = %+v, want a new task", resp)
	}
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("new task = %+v, want one reply", task)
	}
	wantChannels(t, "superseding panel message", env.actionChannels(t, task), store.ChannelPanel)
	if old := env.waitTask(t, first.TaskID); old.Status != conloomapi.TaskCancelled || len(old.Actions) != 0 {
		t.Errorf("superseded task = %+v, want cancelled without a reply", old)
	}
}

func TestDeletePanelChat_SubmitWaitsAndFindsNothing(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ctx := context.Background()
	key, err := env.o.CreatePanelChat(ctx, testBot, "admin", "小明")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := env.blockFirstCompletion(t, func(store.TaskCompletion) bool { return true })
	taskID := env.panelResponse(t, env.submitPanel(t, key, "admin", "你好")).TaskID
	waitEntered(t, entered)

	deleted := make(chan error, 1)
	go func() { deleted <- env.o.DeletePanelChat(ctx, key) }()
	waitUntil(t, "the delete to cancel the running task", func() bool {
		env.o.mu.Lock()
		defer env.o.mu.Unlock()
		ow := env.o.owners[taskID]
		return ow != nil && ow.cancelled
	})
	submitted := make(chan error, 1)
	go func() {
		_, _, err := env.o.SubmitPanel(ctx, key, "admin", "还在吗")
		submitted <- err
	}()
	select {
	case err := <-submitted:
		t.Fatalf("SubmitPanel returned %v while the chat was being deleted", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	if err := <-deleted; err != nil {
		t.Fatalf("DeletePanelChat: %v", err)
	}
	if err := <-submitted; !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SubmitPanel after the delete: err = %v, want store.ErrNotFound", err)
	}
	if env.o.Busy(key) {
		t.Error("Busy after delete")
	}
	if _, err := env.st.GetConversation(ctx, testBot, strings.Split(key, ":")[3]); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("conversation after delete: %v", err)
	}
}

func TestDeletePanelChat_KeepsChatWhileATaskStillRuns(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.o.panelDeleteWait = 50 * time.Millisecond
	ctx := context.Background()
	key, err := env.o.CreatePanelChat(ctx, testBot, "admin", "小明")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := env.blockFirstCompletion(t, func(store.TaskCompletion) bool { return true })
	taskID := env.panelResponse(t, env.submitPanel(t, key, "admin", "你好")).TaskID
	waitEntered(t, entered)
	if err := env.o.DeletePanelChat(ctx, key); !errors.Is(err, ErrChatBusy) {
		t.Fatalf("DeletePanelChat while a task of the chat still runs: err = %v, want ErrChatBusy", err)
	}
	if _, err := env.st.GetConversation(ctx, testBot, strings.Split(key, ":")[3]); err != nil {
		t.Fatalf("conversation after a refused delete: %v", err)
	}
	if err := env.o.DeletePanelChat(ctx, key); !errors.Is(err, ErrChatBusy) {
		t.Fatalf("retry while the task still runs: err = %v, want ErrChatBusy", err)
	}
	env.o.panelDeleteWait = waitLimit
	go func() {
		time.Sleep(20 * time.Millisecond)
		release()
	}()
	if err := env.o.DeletePanelChat(ctx, key); err != nil {
		t.Fatalf("DeletePanelChat once the task stopped: %v", err)
	}
	if _, err := env.st.GetTask(ctx, testBot, taskID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("task after delete: %v", err)
	}
}

func TestDeletePanelChat_DoesNotBlockOtherChats(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.o.panelDeleteWait = waitLimit
	ctx := context.Background()
	key, err := env.o.CreatePanelChat(ctx, testBot, "admin", "小明")
	if err != nil {
		t.Fatal(err)
	}
	// other's key shares a mutex with key's in a table of 64 hashed mutexes
	// (FNV-32a mod 64, like keyedLocks), so the test fails should the panel
	// locks go back to hashed slots.
	slot := func(k string) uint32 {
		h := fnv.New32a()
		_, _ = h.Write([]byte(k))
		return h.Sum32() % 64
	}
	var other string
	for range 5000 {
		k, err := env.o.CreatePanelChat(ctx, testBot, "admin", "小红")
		if err != nil {
			t.Fatal(err)
		}
		if slot(k) == slot(key) {
			other = k
			break
		}
	}
	if other == "" {
		t.Fatal("no chat key shares a hash slot with the first")
	}

	entered, release := env.blockFirstCompletion(t, func(store.TaskCompletion) bool { return true })
	defer release()
	taskID := env.panelResponse(t, env.submitPanel(t, key, "admin", "你好")).TaskID
	waitEntered(t, entered)
	deleted := make(chan error, 1)
	go func() { deleted <- env.o.DeletePanelChat(ctx, key) }()
	waitUntil(t, "the delete to cancel the running task", func() bool {
		env.o.mu.Lock()
		defer env.o.mu.Unlock()
		ow := env.o.owners[taskID]
		return ow != nil && ow.cancelled
	})

	submitted := make(chan error, 1)
	go func() {
		_, _, err := env.o.SubmitPanel(ctx, other, "admin", "在吗")
		submitted <- err
	}()
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatalf("SubmitPanel to another chat: %v", err)
		}
	case err := <-deleted:
		t.Fatalf("DeletePanelChat returned %v before the other chat's SubmitPanel", err)
	case <-time.After(waitLimit / 2):
		t.Fatal("SubmitPanel to another chat waited for the delete of the first")
	}
	release()
	if err := <-deleted; err != nil {
		t.Fatalf("DeletePanelChat: %v", err)
	}
}

func TestLockTable(t *testing.T) {
	var l lockTable
	unlockA := l.lock("a")
	unlockB := l.lock("b")

	acquired := make(chan struct{})
	go func() {
		unlock := l.lock("a")
		close(acquired)
		unlock()
	}()
	select {
	case <-acquired:
		t.Fatal("a second lock of a key was acquired while the first was held")
	case <-time.After(20 * time.Millisecond):
	}
	unlockB()
	unlockA()
	select {
	case <-acquired:
	case <-time.After(waitLimit):
		t.Fatal("the waiting lock was not acquired after the unlock")
	}
	waitUntil(t, "the table to drop its unused entries", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return len(l.locks) == 0
	})
}

// startPanelDeliverer starts a panel deliverer for a test and returns it, so
// tests can wake it after a task finishes.
func (e *testEnv) startPanelDeliverer(t *testing.T) *outbox.PanelDeliverer {
	t.Helper()
	svc := outbox.New(e.st, outbox.Config{}, nil, nil)
	d := outbox.NewPanelDeliverer(svc, e.st, time.Hour, nil, nil)
	d.Start()
	t.Cleanup(d.Close)
	d.Wake()
	return d
}

// panelMessage returns the stored message id.
func (e *testEnv) panelMessage(t *testing.T, id string) store.MessageRecord {
	t.Helper()
	m, err := e.st.GetMessage(context.Background(), testBot, id)
	if err != nil {
		t.Fatalf("GetMessage(%s): %v", id, err)
	}
	return m
}

// waitPanelReply wakes d and waits for a bot message after seq.
func (e *testEnv) waitPanelReply(t *testing.T, d *outbox.PanelDeliverer, key string, after int64) store.MessageRecord {
	t.Helper()
	d.Wake()
	var got store.MessageRecord
	waitUntil(t, "panel bot reply", func() bool {
		msgs, err := e.st.MessagesAfter(context.Background(), key, after, 100)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.FromBot {
				got = m
				return true
			}
		}
		return false
	})
	return got
}

func TestWithdrawPanel_DeletesFromSeqToEnd(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)

	firstID := env.submitPanel(t, key, "admin", "第一句")
	env.waitTask(t, env.panelResponse(t, firstID).TaskID)
	firstUser := env.panelMessage(t, firstID)
	firstBot := env.waitPanelReply(t, d, key, firstUser.Seq)

	secondID := env.submitPanel(t, key, "admin", "第二句")
	env.waitTask(t, env.panelResponse(t, secondID).TaskID)
	secondUser := env.panelMessage(t, secondID)
	env.waitPanelReply(t, d, key, secondUser.Seq)

	deleted, err := env.o.WithdrawPanel(ctx, key, secondUser.Seq)
	if err != nil {
		t.Fatalf("WithdrawPanel: %v", err)
	}
	if deleted < 2 {
		t.Errorf("deleted = %d, want at least the user message and its reply", deleted)
	}
	msgs, err := env.st.MessagesAfter(ctx, key, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].ID != firstID || msgs[1].ID != firstBot.ID {
		t.Fatalf("messages after withdraw = %+v, want the first user and bot only", msgs)
	}
	if env.o.Busy(key) {
		t.Error("the chat is busy after withdrawing")
	}
}

func TestRegeneratePanel_ReusesTheUserMessage(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)

	id := env.submitPanel(t, key, "admin", "第一句")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	user := env.panelMessage(t, id)
	oldBot := env.waitPanelReply(t, d, key, user.Seq)

	before := time.Now().Add(-time.Second)
	taskID, err := env.o.RegeneratePanel(ctx, key, oldBot.Seq)
	if err != nil {
		t.Fatalf("RegeneratePanel: %v", err)
	}
	task, err := env.st.GetTask(ctx, testBot, taskID)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if len(task.TriggerMessageIDs) != 1 || task.TriggerMessageIDs[0] != user.ID {
		t.Errorf("trigger messages = %v, want [%s]", task.TriggerMessageIDs, user.ID)
	}
	if task.EventOccurredAt.Before(before) {
		t.Errorf("task EventOccurredAt = %v, want near now", task.EventOccurredAt)
	}
	if _, err := env.st.GetMessage(ctx, testBot, oldBot.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old bot message err = %v, want ErrNotFound", err)
	}
	env.waitTask(t, taskID)
	env.waitPanelReply(t, d, key, user.Seq)

	msgs, err := env.st.MessagesAfter(ctx, key, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	users, bots := 0, 0
	for _, m := range msgs {
		if m.FromBot {
			bots++
		} else {
			users++
			if m.ID != user.ID {
				t.Errorf("user message = %s, want %s", m.ID, user.ID)
			}
		}
	}
	if users != 1 || bots == 0 {
		t.Fatalf("messages = %d user, %d bot; want 1 user and a new reply", users, bots)
	}
}

func TestRegeneratePanel_RefusesOlderReply(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)

	firstID := env.submitPanel(t, key, "admin", "第一句")
	env.waitTask(t, env.panelResponse(t, firstID).TaskID)
	firstUser := env.panelMessage(t, firstID)
	firstBot := env.waitPanelReply(t, d, key, firstUser.Seq)

	secondID := env.submitPanel(t, key, "admin", "第二句")
	env.waitTask(t, env.panelResponse(t, secondID).TaskID)
	secondUser := env.panelMessage(t, secondID)
	env.waitPanelReply(t, d, key, secondUser.Seq)

	before, err := env.st.MessagesAfter(ctx, key, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.o.RegeneratePanel(ctx, key, firstBot.Seq); !errors.Is(err, ErrNotLatest) {
		t.Fatalf("RegeneratePanel older reply err = %v, want ErrNotLatest", err)
	}
	after, err := env.st.MessagesAfter(ctx, key, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("messages changed from %d to %d after a refused regenerate", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID {
			t.Fatalf("message %d changed from %s to %s", i, before[i].ID, after[i].ID)
		}
	}
}

func TestWithdrawPanel_RefusesBusy(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	entered, release := env.blockFirstCompletion(t, func(store.TaskCompletion) bool { return true })
	id := env.submitPanel(t, key, "admin", "等等")
	waitEntered(t, entered)
	user := env.panelMessage(t, id)

	if _, err := env.o.WithdrawPanel(ctx, key, user.Seq); !errors.Is(err, ErrChatNotIdle) {
		t.Fatalf("WithdrawPanel while busy err = %v, want ErrChatNotIdle", err)
	}
	if _, err := env.st.GetMessage(ctx, testBot, id); err != nil {
		t.Errorf("user message was deleted while the chat was busy: %v", err)
	}
	release()
	env.waitTask(t, env.panelResponse(t, id).TaskID)
}

func TestWithdrawPanel_RefusesPendingPart(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	id := env.submitPanel(t, key, "admin", "等等")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	user := env.panelMessage(t, id)
	// Without a panel deliverer the reply action is stored but not delivered.
	if _, err := env.o.WithdrawPanel(ctx, key, user.Seq); !errors.Is(err, ErrChatNotIdle) {
		t.Fatalf("WithdrawPanel with a pending part err = %v, want ErrChatNotIdle", err)
	}
	if _, err := env.st.GetMessage(ctx, testBot, id); err != nil {
		t.Errorf("user message was deleted while a part was pending: %v", err)
	}
}

func TestRegeneratePanel_RefusesBusy(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	id := env.submitPanel(t, key, "admin", "第一句")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	user := env.panelMessage(t, id)
	bot := env.waitPanelReply(t, d, key, user.Seq)

	entered, release := env.blockFirstCompletion(t, func(store.TaskCompletion) bool { return true })
	second := env.submitPanel(t, key, "admin", "第二句")
	waitEntered(t, entered)
	if _, err := env.o.RegeneratePanel(ctx, key, bot.Seq); !errors.Is(err, ErrChatNotIdle) {
		t.Fatalf("RegeneratePanel while busy err = %v, want ErrChatNotIdle", err)
	}
	release()
	env.waitTask(t, env.panelResponse(t, second).TaskID)
}

func TestRegeneratePanel_RefusesPaused(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	id := env.submitPanel(t, key, "admin", "第一句")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	user := env.panelMessage(t, id)
	bot := env.waitPanelReply(t, d, key, user.Seq)

	pol := conloomapi.DefaultGroupPolicy()
	pol.Concurrency.MaxInteractiveRequests = 0
	if _, err := env.st.AppendPolicyVersion(ctx, store.PolicyVersion{
		ConversationKey: key, Policy: pol, Reason: "pause", UpdatedBy: "test", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	env.o.policies.Forget(key)
	if _, err := env.o.RegeneratePanel(ctx, key, bot.Seq); !errors.Is(err, errPaused) {
		t.Fatalf("RegeneratePanel paused err = %v, want errPaused", err)
	}
	if _, err := env.st.GetMessage(ctx, testBot, bot.ID); err != nil {
		t.Errorf("bot message was deleted while paused: %v", err)
	}
}

func TestRegeneratePanel_RefusesUserMessage(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	id := env.submitPanel(t, key, "admin", "第一句")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	user := env.panelMessage(t, id)
	env.waitPanelReply(t, d, key, user.Seq)

	if _, err := env.o.RegeneratePanel(ctx, key, user.Seq); !errors.Is(err, ErrNotLatest) {
		t.Fatalf("RegeneratePanel on a user message err = %v, want ErrNotLatest", err)
	}
	if _, err := env.st.GetMessage(ctx, testBot, id); err != nil {
		t.Errorf("user message was deleted: %v", err)
	}
}

func TestRegeneratePanel_RefusesWithoutUserMessage(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	now := time.Now().UTC()
	m := store.MessageRecord{
		ID: "msg_bot_orphan", BotInstanceID: testBot, ConversationKey: key, Platform: store.PlatformPanel,
		PlatformMessageID: "panel_bot_orphan", FromBot: true, Text: "只有我", Channel: store.ChannelPanel,
		OccurredAt: now, CreatedAt: now,
	}
	if _, err := env.st.Ingest(ctx, store.Ingest{
		Event:   store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed_orphan", EventID: "evt_seed_orphan", CreatedAt: now},
		Message: &m,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := env.st.GetMessage(ctx, testBot, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.o.RegeneratePanel(ctx, key, got.Seq); !errors.Is(err, ErrNotReply) {
		t.Fatalf("RegeneratePanel without a user message err = %v, want ErrNotReply", err)
	}
	if _, err := env.st.GetMessage(ctx, testBot, m.ID); err != nil {
		t.Errorf("bot message was deleted: %v", err)
	}
}

func TestRegeneratePanel_RefusesCommandReply(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	id := env.submitPanel(t, key, "admin", "/help")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	user := env.panelMessage(t, id)
	bot := env.waitPanelReply(t, d, key, user.Seq)

	if _, err := env.o.RegeneratePanel(ctx, key, bot.Seq); !errors.Is(err, ErrCommandReply) {
		t.Fatalf("RegeneratePanel on a command reply err = %v, want ErrCommandReply", err)
	}
	if _, err := env.st.GetMessage(ctx, testBot, bot.ID); err != nil {
		t.Errorf("command reply was deleted: %v", err)
	}
}

// seedBotTurn stores a finished task of the panel chat key with trigger and
// triggers whose reply is text, as a wake or a reply task would, delivers it
// through d and returns the bot message.
func (e *testEnv) seedBotTurn(t *testing.T, d *outbox.PanelDeliverer, key, trigger string, triggers []string, text string) store.MessageRecord {
	t.Helper()
	ctx := context.Background()
	conv, err := e.o.panelConversation(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	newest, err := e.o.newestSeq(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	id := ids.New("task")
	task := store.TaskRecord{ID: id, BotInstanceID: testBot, ConversationKey: key, Status: conloomapi.TaskRunning,
		ActorPlatformUserID: PanelActorID("admin"), TriggerMessageIDs: triggers, Priority: int(admission.PriorityBackground),
		Meta: conloomapi.TaskMeta{Trigger: trigger}, EventOccurredAt: now, CreatedAt: now, UpdatedAt: now}
	if _, err := e.st.Ingest(ctx, store.Ingest{
		Event:   store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed:" + id, EventID: "seed_" + id, Response: []byte(`{}`), CreatedAt: now},
		NewTask: &task,
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CompleteTask(ctx, store.TaskCompletion{TaskID: id, Status: conloomapi.TaskSucceeded, Meta: task.Meta,
		Actions: []store.OutboxRecord{e.o.newAction(ctx, task, conv, nil, text)}}); err != nil {
		t.Fatal(err)
	}
	return e.waitPanelReply(t, d, key, newest)
}

// chatIDs lists the IDs of the panel chat's messages in order.
func (e *testEnv) chatIDs(t *testing.T, key string) []string {
	t.Helper()
	msgs, err := e.st.MessagesAfter(context.Background(), key, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

// regenerableSeq is what the panel shows 重新生成 on.
func (e *testEnv) regenerableSeq(t *testing.T, key string) int64 {
	t.Helper()
	seq, err := e.o.RegenerableSeq(context.Background(), key)
	if err != nil {
		t.Fatalf("RegenerableSeq: %v", err)
	}
	return seq
}

// A message the bot sent on its own is not a reply: regenerating it would
// delete the reply before it too and answer the old message again.
func TestRegeneratePanel_RefusesWakeMessage(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	id := env.submitPanel(t, key, "admin", "第一句")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	user := env.panelMessage(t, id)
	reply := env.waitPanelReply(t, d, key, user.Seq)
	if got := env.regenerableSeq(t, key); got != reply.Seq {
		t.Errorf("RegenerableSeq after a reply = %d, want %d", got, reply.Seq)
	}

	for _, trigger := range []string{TriggerInitiate, TriggerFollowUp, TriggerStatusReturn, TriggerProactive} {
		wake := env.seedBotTurn(t, d, key, trigger, nil, "在吗")
		before := env.chatIDs(t, key)
		if _, err := env.o.RegeneratePanel(ctx, key, wake.Seq); !errors.Is(err, ErrNotReply) {
			t.Errorf("RegeneratePanel on a %s message err = %v, want ErrNotReply", trigger, err)
		}
		if after := env.chatIDs(t, key); !slices.Equal(after, before) {
			t.Errorf("messages after a refused regenerate of a %s message = %v, want %v", trigger, after, before)
		}
		if got := env.regenerableSeq(t, key); got != 0 {
			t.Errorf("RegenerableSeq after a %s message = %d, want 0", trigger, got)
		}
	}
}

// A reply to several messages merged into one turn is regenerated for all of
// them, and only the reply is deleted.
func TestRegeneratePanel_KeepsAllMergedTriggers(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privateWait(300 * time.Millisecond)})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	firstID := env.submitPanel(t, key, "admin", "第一句")
	secondID := env.submitPanel(t, key, "admin", "第二句")
	taskID := env.panelResponse(t, firstID).TaskID
	if got := env.panelResponse(t, secondID).TaskID; got != taskID {
		t.Fatalf("second message went to task %q, want it merged into %q", got, taskID)
	}
	env.waitTask(t, taskID)
	second := env.panelMessage(t, secondID)
	reply := env.waitPanelReply(t, d, key, second.Seq)

	newID, err := env.o.RegeneratePanel(ctx, key, reply.Seq)
	if err != nil {
		t.Fatalf("RegeneratePanel: %v", err)
	}
	task, err := env.st.GetTask(ctx, testBot, newID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{firstID, secondID}; !slices.Equal(task.TriggerMessageIDs, want) {
		t.Errorf("trigger messages = %v, want %v", task.TriggerMessageIDs, want)
	}
	if got := env.chatIDs(t, key); !slices.Equal(got, []string{firstID, secondID}) {
		t.Errorf("messages after regenerating = %v, want both user messages and no reply", got)
	}
	env.waitTask(t, newID)
}

// After the user was forgotten, a new reply would answer nothing: the
// runner drops the messages from before the forget.
func TestRegeneratePanel_RefusesAfterForget(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	id := env.submitPanel(t, key, "admin", "第一句")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	user := env.panelMessage(t, id)
	reply := env.waitPanelReply(t, d, key, user.Seq)
	if err := env.st.ForgetPrivateScope(ctx, memory.ScopeKeyOf(key), []string{key}, PanelActorID("admin"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.o.RegeneratePanel(ctx, key, reply.Seq); !errors.Is(err, ErrForgotten) {
		t.Fatalf("RegeneratePanel after a forget err = %v, want ErrForgotten", err)
	}
	if _, err := env.st.GetMessage(ctx, testBot, reply.ID); err != nil {
		t.Errorf("reply was deleted: %v", err)
	}
	if got := env.regenerableSeq(t, key); got != 0 {
		t.Errorf("RegenerableSeq after a forget = %d, want 0", got)
	}
}

// A seq that is not a message of the chat is told apart from a missing chat.
func TestPanelMessageActions_UnknownMessage(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	other := env.createChat(t, testBot)
	id := env.submitPanel(t, other, "admin", "别的聊天")
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	seq := env.panelMessage(t, id).Seq
	for _, s := range []int64{seq, 999999} {
		if _, err := env.o.WithdrawPanel(ctx, key, s); !errors.Is(err, ErrMessageNotFound) || !errors.Is(err, store.ErrNotFound) {
			t.Errorf("WithdrawPanel(%d) err = %v, want ErrMessageNotFound", s, err)
		}
		if _, err := env.o.RegeneratePanel(ctx, key, s); !errors.Is(err, ErrMessageNotFound) {
			t.Errorf("RegeneratePanel(%d) err = %v, want ErrMessageNotFound", s, err)
		}
	}
	gone := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_missing")
	if _, err := env.o.WithdrawPanel(ctx, gone, 1); !errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrMessageNotFound) {
		t.Errorf("WithdrawPanel in a missing chat err = %v, want a plain ErrNotFound", err)
	}
}

// A wake timer due while a message is withdrawn or regenerated starts no wake
// task in between the busy check and the change, as while a message of the
// user is accepted.
func TestPanelMessageActions_NoWakeStartsMeanwhile(t *testing.T) {
	for _, action := range []string{"withdraw", "regenerate"} {
		t.Run(action, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
			ctx := context.Background()
			key := env.createChat(t, testBot)
			d := env.startPanelDeliverer(t)
			id := env.submitPanel(t, key, "admin", "第一句")
			env.waitTask(t, env.panelResponse(t, id).TaskID)
			user := env.panelMessage(t, id)
			reply := env.waitPanelReply(t, d, key, user.Seq)
			timer := store.ConversationTimer{ConversationKey: key, Kind: store.TimerInitiate, BotInstanceID: testBot,
				PlatformUserID: PanelActorID("admin"), DueAt: time.Now().Add(-time.Minute), AnchorSeq: user.Seq}
			if err := env.st.ArmTimer(ctx, timer); err != nil {
				t.Fatal(err)
			}

			var fired atomic.Bool
			var fireErr error
			env.hooks.mu.Lock()
			env.hooks.beforeMessagesBySeq = func() {
				if fired.CompareAndSwap(false, true) {
					fireErr = env.o.fireTimer(ctx, timer)
				}
			}
			env.hooks.mu.Unlock()
			var err error
			if action == "withdraw" {
				_, err = env.o.WithdrawPanel(ctx, key, reply.Seq)
			} else {
				_, err = env.o.RegeneratePanel(ctx, key, reply.Seq)
			}
			if err != nil {
				t.Fatalf("%s: %v", action, err)
			}
			if !fired.Load() {
				t.Fatal("the timer was not fired during the action")
			}
			if !errors.Is(fireErr, errUserSpeaking) {
				t.Errorf("fireTimer during %s = %v, want errUserSpeaking", action, fireErr)
			}
			for _, qt := range env.o.sched.Active(key) {
				if qt.ActorID == wakeActor(PanelActorID("admin")) {
					t.Errorf("wake task %s started during %s", qt.TaskID, action)
				}
			}
		})
	}
}
