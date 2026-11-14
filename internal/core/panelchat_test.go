package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
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
	id, err := e.o.SubmitPanel(context.Background(), convKey, panelUser, text)
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

func TestSubmitPanel_TakeoverRepliesOnPanelOnly(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := privateKey("20001")

	first := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗")).TaskID)
	wantChannels(t, "platform message", env.actionChannels(t, first), "")
	platformAction := first.Actions[0].ActionID

	id := env.submitPanel(t, key, "admin", "今天过得怎么样")
	msg, err := env.st.GetMessage(ctx, testBot, id)
	if err != nil {
		t.Fatalf("panel message not stored: %v", err)
	}
	if msg.Channel != store.ChannelPanel || msg.PlatformUserID != "20001" || msg.ActorID != "user_20001" ||
		msg.DisplayName != "用户20001" || msg.Platform != "qq" || msg.ConversationKey != key || msg.FromBot {
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

func TestSubmitPanel_MergedTaskTakesNewestTriggerChannel(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privateWait(400 * time.Millisecond)})

	resp := env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "第一句"))
	merged := env.panelResponse(t, env.submitPanel(t, privateKey("20001"), "admin", "第二句"))
	if !merged.Merged || merged.TaskID != resp.TaskID {
		t.Fatalf("panel message response = %+v, want merged into %s", merged, resp.TaskID)
	}
	wantChannels(t, "platform then panel", env.actionChannels(t, env.waitTask(t, resp.TaskID)), store.ChannelPanel)

	resp = env.submit(t, env.event(conloomapi.ConvPrivate, "20002", "20002", "第一句"))
	merged = env.panelResponse(t, env.submitPanel(t, privateKey("20002"), "admin", "第二句"))
	last := env.submit(t, env.event(conloomapi.ConvPrivate, "20002", "20002", "第三句"))
	if !merged.Merged || !last.Merged || merged.TaskID != resp.TaskID || last.TaskID != resp.TaskID {
		t.Fatalf("responses %+v, %+v; want both merged into %s", merged, last, resp.TaskID)
	}
	wantChannels(t, "panel then platform", env.actionChannels(t, env.waitTask(t, resp.TaskID)), "")
}

func TestSubmitPanel_RateLimited(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		allowAllPrivate(c)
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 1
	}})
	env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗")).TaskID)
	resp := env.panelResponse(t, env.submitPanel(t, privateKey("20001"), "admin", "还在吗"))
	if resp.Disposition != conloomapi.DispositionObserve || resp.SuppressedReason != conloomapi.SuppressedRateLimited {
		t.Fatalf("panel message response = %+v, want observed as rate limited", resp)
	}
}

func TestSubmitPanel_Errors(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	env.submit(t, env.groupEvent("20001", "大家好"))
	env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗")).TaskID)
	emptyKey := privateKey("20002")
	if _, err := env.st.Ingest(ctx, store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed_20002", EventID: "evt_seed_20002"},
		Conversation: &store.ConversationRecord{BotInstanceID: testBot, ID: "private_20002", Key: emptyKey,
			Type: conloomapi.ConvPrivate, Platform: "qq", PlatformConversationID: "20002"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, key, text string
		want            error
	}{
		{"unknown conversation", privateKey("20009"), "你好", store.ErrNotFound},
		{"malformed key", "bot_main:qq", "你好", store.ErrNotFound},
		{"group", testConvKey, "你好", ErrNotPrivate},
		{"no user message", emptyKey, "你好", ErrNoUserYet},
		{"empty", privateKey("20001"), "", ErrBadText},
		{"blank", privateKey("20001"), " \n ", ErrBadText},
		{"too long", privateKey("20001"), strings.Repeat("长", 2001), ErrBadText},
	} {
		if _, err := env.o.SubmitPanel(ctx, c.key, "admin", c.text); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := env.o.SubmitPanel(ctx, privateKey("20001"), "admin", strings.Repeat("长", 2000)); err != nil {
		t.Errorf("2000 characters: %v", err)
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
		{"newest from the panel", platform, []store.MessageRecord{fromPlatform, fromPanel}, store.ChannelPanel},
		{"newest from the platform", platform, []store.MessageRecord{fromPanel, fromPlatform}, ""},
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
