package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/panel"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type chatMessage struct {
	Seq      int64     `json:"seq"`
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	SenderID string    `json:"sender_id"`
	FromBot  bool      `json:"from_bot"`
	Text     string    `json:"text"`
	Channel  string    `json:"channel"`
}

type chatPoll struct {
	Messages  []chatMessage `json:"messages"`
	Typing    bool          `json:"typing"`
	NextAfter int64         `json:"next_after"`
}

func convAPIPath(key string, rest ...string) string {
	return strings.Join(append([]string{"/panel/api/conversations/" + url.PathEscape(key)}, rest...), "/")
}

// chat polls the conversation's chat after the seq cursor after.
func (c *panelClient) chat(key string, after int64) chatPoll {
	c.t.Helper()
	var p chatPoll
	c.get(fmt.Sprintf("%s?after=%d", convAPIPath(key, "chat"), after), &p)
	return p
}

// send writes text to the conversation from the panel and returns the new
// message's ID.
func (c *panelClient) send(key, text string) string {
	c.t.Helper()
	status, raw := c.do(http.MethodPost, convAPIPath(key, "messages"), map[string]string{"text": text})
	var resp struct {
		MessageID string `json:"message_id"`
	}
	if status != http.StatusAccepted || json.Unmarshal(raw, &resp) != nil || resp.MessageID == "" {
		c.t.Fatalf("send %q to %s: status %d, body %s", text, key, status, raw)
	}
	return resp.MessageID
}

// botReplies polls the chat from after until it shows n bot messages after
// the cursor and the bot is no longer typing, and returns those messages.
// While the chat shows some but not all of them, the bot must be typing.
func (c *panelClient) botReplies(key string, after int64, n int) []chatMessage {
	c.t.Helper()
	var bot []chatMessage
	var typing bool
	deadline := time.Now().Add(drainWait)
	for len(bot) < n || typing {
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out waiting for %d bot messages in %s and typing to end: got %+v, typing %v", n, key, bot, typing)
		}
		time.Sleep(20 * time.Millisecond)
		p := c.chat(key, after)
		bot = bot[:0]
		for _, m := range p.Messages {
			if m.FromBot {
				bot = append(bot, m)
			}
		}
		typing = p.Typing
		if len(bot) > 0 && len(bot) < n && !typing {
			c.t.Fatalf("chat %s shows %d of %d bot messages but the bot is not typing", key, len(bot), n)
		}
		if len(bot) > n {
			c.t.Fatalf("chat %s shows bot messages %+v, want %d", key, bot, n)
		}
	}
	return bot
}

// watchAdapter long-polls the outbox as the bot's adapter does until the
// returned stop is called; stop returns the actions leased meanwhile, which
// are never acknowledged. If the test ends first, the watch is stopped and
// any action it leased fails the test.
func (e *env) watchAdapter() (stop func() []conloomapi.Action) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []conloomapi.Action, 1)
	go func() {
		var leased []conloomapi.Action
		for ctx.Err() == nil {
			actions, err := e.client.PollOutbox(ctx, 0, time.Second)
			if err != nil && ctx.Err() == nil {
				e.t.Errorf("adapter poll: %v", err)
				break
			}
			leased = append(leased, actions...)
		}
		done <- leased
	}()
	var leased []conloomapi.Action
	stopped := false
	stop = func() []conloomapi.Action {
		if !stopped {
			stopped = true
			cancel()
			leased = <-done
		}
		return leased
	}
	e.t.Cleanup(func() {
		if !stopped {
			if leased := stop(); len(leased) != 0 {
				e.t.Errorf("the adapter leased %+v", leased)
			}
		}
	})
	return stop
}

// panelPartSlack is how late after the part before it plus its typing time a
// reply part may reach the panel.
const panelPartSlack = 500 * time.Millisecond

func TestPanelChatE2E(t *testing.T) {
	const hotpot = "今天吃了火锅"
	parts := []string{"火锅好吃吗", "下次也带我去吧，我想吃毛肚和鸭肠，再来一份冰粉"}
	fake := fakeopenai.New(t, func(req map[string]any) (int, string) {
		if strings.Contains(requestText(req), hotpot) {
			return 200, strings.Join(parts, "\n|||\n")
		}
		return 200, fakeReply
	})
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	cfg := panelTestConfig(t, configParams{modelURL: fake.URL}, hash)
	setPrivateRhythm(t, cfg, 50*time.Millisecond, 2)
	// The panel deliverer only delivers when woken, so reply parts reach the
	// panel only through the core's notifications.
	e := newEnvWith(t, cfg, app.Options{PanelDeliveryInterval: time.Hour})
	p := panelLogin(t, e.srv.URL)

	// A new panel chat answers in the panel only, paced like any private chat.
	status, raw := p.do(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": botID, "display_name": "小明"})
	var created struct {
		Key string `json:"key"`
	}
	if status != http.StatusCreated || json.Unmarshal(raw, &created) != nil || created.Key == "" {
		t.Fatalf("create chat: status %d, body %s", status, raw)
	}
	stop := e.watchAdapter()
	p.send(created.Key, hotpot)
	got := p.botReplies(created.Key, 0, len(parts))
	if leased := stop(); len(leased) != 0 {
		t.Fatalf("the adapter leased %+v, want no reply of the panel chat", leased)
	}
	for i, m := range got {
		if m.Text != parts[i] || m.Channel != store.ChannelPanel || !strings.HasPrefix(m.ID, "msg_panel_") {
			t.Errorf("bot message %d = %+v, want %q on the panel channel, delivered by the panel", i+1, m, parts[i])
		}
	}
	all := p.chat(created.Key, 0)
	if len(all.Messages) != 3 || all.Messages[0].FromBot || all.Messages[0].Text != hotpot ||
		all.Messages[0].Channel != store.ChannelPanel || all.Messages[0].SenderID != "panel_"+panelUser {
		t.Fatalf("chat = %+v, want the panel message and two bot messages", all.Messages)
	}
	sent := all.Messages[0].At
	if held := got[1].At.Sub(sent); held < typingTime(parts[1]) {
		t.Errorf("second part delivered %v after the message, want at least its typing time %v", held, typingTime(parts[1]))
	}
	if gap, due := got[1].At.Sub(got[0].At), typingTime(parts[1]); gap > due+panelPartSlack {
		t.Errorf("second part delivered %v after the first, want within %v of its typing time %v", gap, panelPartSlack, due)
	}

	// The panel cannot write in a platform chat: nothing is stored, answered
	// or handed to the adapter.
	e.say(adminChat, admin, "晚上好", mock.SayOptions{})
	if a := e.drainOne(); a.Content.Text != fakeReply {
		t.Fatalf("platform reply = %q, want %q", a.Content.Text, fakeReply)
	}
	platformChat := store.ConversationKey(botID, "qq", conloomapi.ConvPrivate, "private_"+admin.PlatformID)
	cursor := p.chat(platformChat, 0).NextAfter
	calls := len(fake.Requests())
	stop = e.watchAdapter()
	if status, raw := p.do(http.MethodPost, convAPIPath(platformChat, "messages"), map[string]string{"text": "你好"}); status != http.StatusBadRequest {
		t.Fatalf("message to the platform chat: status %d, body %s; want 400", status, raw)
	}
	if tail := p.chat(platformChat, cursor); len(tail.Messages) != 0 || tail.Typing {
		t.Fatalf("platform chat after the refused message = %+v, want nothing new", tail)
	}
	if leased := stop(); len(leased) != 0 {
		t.Fatalf("the adapter leased %+v after the refused message", leased)
	}
	if n := len(fake.Requests()); n != calls {
		t.Fatalf("model requests = %d after the refused message, want %d", n, calls)
	}

	// Deleting the panel chat removes it; platform chats cannot be deleted.
	if status, raw := p.do(http.MethodDelete, convAPIPath(platformChat), nil); status != http.StatusBadRequest {
		t.Fatalf("delete platform chat: status %d, body %s; want 400", status, raw)
	}
	if status, raw := p.do(http.MethodDelete, convAPIPath(created.Key), nil); status != http.StatusNoContent {
		t.Fatalf("delete chat: status %d, body %s", status, raw)
	}
	if status, raw := p.do(http.MethodGet, convAPIPath(created.Key), nil); status != http.StatusNotFound {
		t.Fatalf("GET deleted chat: status %d, body %s; want 404", status, raw)
	}
	var conv struct {
		Key string `json:"key"`
	}
	if p.get(convAPIPath(platformChat), &conv); conv.Key != platformChat {
		t.Fatalf("platform chat after the delete = %+v, want %s", conv, platformChat)
	}
}

func TestPanelChatE2E_StoredPanelBotLoadedAtStart(t *testing.T) {
	const panelBot = "panel-e2e00001"
	fake := fakeopenai.New(t, answer(fakeReply))
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	cfg := panelTestConfig(t, configParams{modelURL: fake.URL}, hash)
	if err := os.MkdirAll(cfg.Storage.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := sqlite.Open(ctx, cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePanelBot(ctx, store.PanelBot{ID: panelBot, Name: "小红", PersonaTemplate: "default",
		Skills: []string{"time"}, CreatedBy: "panel:" + panelUser}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	e := newEnv(t, cfg)
	p := panelLogin(t, e.srv.URL)
	var view struct {
		Config struct {
			Bots []struct {
				ID     string   `json:"id"`
				Name   string   `json:"name"`
				Source string   `json:"source"`
				Skills []string `json:"skills"`
			} `json:"bots"`
		} `json:"config"`
	}
	p.get("/panel/api/config", &view)
	bots := view.Config.Bots
	if n := len(bots); n == 0 || bots[n-1].ID != panelBot || bots[n-1].Name != "小红" ||
		bots[n-1].Source != "panel" || !slices.Equal(bots[n-1].Skills, []string{"time"}) {
		t.Fatalf("config view bots = %+v, want the stored panel bot last", bots)
	}

	status, raw := p.do(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": panelBot, "display_name": "小明"})
	var created struct {
		Key string `json:"key"`
	}
	if status != http.StatusCreated || json.Unmarshal(raw, &created) != nil || !strings.HasPrefix(created.Key, panelBot+":panel:private:") {
		t.Fatalf("create chat of the panel bot: status %d, body %s", status, raw)
	}
	p.send(created.Key, "你好")
	if got := p.botReplies(created.Key, 0, 1); got[0].Text != fakeReply {
		t.Fatalf("reply = %+v, want %q", got, fakeReply)
	}
	reqs := fake.Requests()
	if sys := requestMessagesOf(reqs[len(reqs)-1])[0]; !strings.HasPrefix(sys, persona.CorePrompt("小红")) {
		t.Errorf("system prompt does not call the bot 小红:\n%s", sys)
	}
}

// suggestMarker is part of the prompt that asks for reply suggestions.
const suggestMarker = "替用户写 3 句"

// suggest asks for the reply suggestions after seq in the chat key and
// returns the status and the suggestions.
func (c *panelClient) suggest(key string, seq int64) (int, []string) {
	c.t.Helper()
	status, raw := c.do(http.MethodPost, convAPIPath(key, "suggestions"), map[string]int64{"after_seq": seq})
	var resp struct {
		Suggestions []string `json:"suggestions"`
	}
	if status == http.StatusOK && (json.Unmarshal(raw, &resp) != nil || resp.Suggestions == nil) {
		c.t.Fatalf("suggestions in %s: body %s", key, raw)
	}
	return status, resp.Suggestions
}

func TestPanelChatE2E_Suggestions(t *testing.T) {
	want := []string{"火锅好吃吗", "下次一起去吧", "最近在看什么书"}
	answerJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	fake := fakeopenai.New(t, func(req map[string]any) (int, string) {
		if strings.Contains(requestText(req), suggestMarker) {
			return 200, "```json\n" + string(answerJSON) + "\n```"
		}
		return 200, fakeReply
	})
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	cfg := panelTestConfig(t, configParams{modelURL: fake.URL}, hash)
	short := cfg.ModelProfiles["short_context"]
	short.ExtraBody = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true}}
	cfg.ModelProfiles["short_context"] = short
	setPrivateRhythm(t, cfg, 50*time.Millisecond, 1)
	e := newEnv(t, cfg)
	p := panelLogin(t, e.srv.URL)

	status, raw := p.do(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": botID, "display_name": "小明"})
	var created struct {
		Key string `json:"key"`
	}
	if status != http.StatusCreated || json.Unmarshal(raw, &created) != nil {
		t.Fatalf("create chat: status %d, body %s", status, raw)
	}
	p.send(created.Key, "今天吃了火锅")
	reply := p.botReplies(created.Key, 0, 1)[0]
	for i := range 2 {
		if status, got := p.suggest(created.Key, reply.Seq); status != http.StatusOK || !slices.Equal(got, want) {
			t.Fatalf("request %d: status %d, suggestions %q; want 200 with %q", i+1, status, got, want)
		}
	}
	var asked []map[string]any
	for _, req := range fake.Requests() {
		if strings.Contains(requestText(req), suggestMarker) {
			asked = append(asked, req)
		}
	}
	if len(asked) != 1 {
		t.Fatalf("model asked for suggestions %d times, want once for both requests", len(asked))
	}
	if kw, _ := asked[0]["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false {
		t.Errorf("suggestions chat_template_kwargs = %v, want thinking off", asked[0]["chat_template_kwargs"])
	}
	if kw, _ := fake.Requests()[0]["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != true {
		t.Errorf("reply chat_template_kwargs = %v, want the profile's thinking on", fake.Requests()[0]["chat_template_kwargs"])
	}

	e.say(adminChat, admin, "晚上好", mock.SayOptions{})
	if a := e.drainOne(); a.Content.Text != fakeReply {
		t.Fatalf("platform reply = %q, want %q", a.Content.Text, fakeReply)
	}
	platformChat := store.ConversationKey(botID, "qq", conloomapi.ConvPrivate, "private_"+admin.PlatformID)
	if status, _ := p.suggest(platformChat, p.chat(platformChat, 0).NextAfter); status != http.StatusBadRequest {
		t.Fatalf("suggestions in the platform chat: status %d, want 400", status)
	}
}

// requestMessagesOf returns the contents of req's messages in order.
func requestMessagesOf(req map[string]any) []string {
	var out []string
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		s, _ := mm["content"].(string)
		out = append(out, s)
	}
	return out
}
