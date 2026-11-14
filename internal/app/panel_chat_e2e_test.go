package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/panel"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type chatMessage struct {
	Seq      int64     `json:"seq"`
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

func TestPanelChatE2E(t *testing.T) {
	const (
		hotpot   = "今天吃了火锅"
		cat      = "我养了一只猫叫团子"
		catReply = "团子这个名字真可爱"
		ask      = "我的猫叫什么"
		answer   = "它叫团子呀"
	)
	parts := []string{"火锅好吃吗", "下次也带我去吧，我想吃毛肚和鸭肠，再来一份冰粉"}
	fake := fakeopenai.New(t, func(req map[string]any) (int, string) {
		prompt := requestText(req)
		switch {
		case strings.Contains(prompt, ask):
			return 200, answer
		case strings.Contains(prompt, cat):
			return 200, catReply
		case strings.Contains(prompt, hotpot):
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
	e := newEnv(t, cfg)
	p := panelLogin(t, e.srv.URL)

	// A new panel chat answers in the panel only, paced like any private chat.
	status, raw := p.do(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": botID, "display_name": "小明"})
	var created struct {
		Key string `json:"key"`
	}
	if status != http.StatusCreated || json.Unmarshal(raw, &created) != nil || created.Key == "" {
		t.Fatalf("create chat: status %d, body %s", status, raw)
	}
	p.send(created.Key, hotpot)
	got := p.botReplies(created.Key, 0, len(parts))
	for i, m := range got {
		if m.Text != parts[i] || m.Channel != store.ChannelPanel {
			t.Errorf("bot message %d = %q on channel %q, want %q on the panel channel", i+1, m.Text, m.Channel, parts[i])
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
	if gap, due := got[1].At.Sub(got[0].At), typingTime(parts[1]); gap > due+partSlack {
		t.Errorf("second part delivered %v after the first, want within %v of its typing time %v", gap, partSlack, due)
	}
	e.requireNoAction(300 * time.Millisecond)

	// Taking over a platform chat: the reply to the panel message stays in
	// the panel, the next platform message is answered on the platform with
	// the panel turn in context.
	e.say(adminChat, admin, "晚上好", mock.SayOptions{})
	if a := e.drainOne(); a.Content.Text != fakeReply {
		t.Fatalf("platform reply = %q, want %q", a.Content.Text, fakeReply)
	}
	takeover := store.ConversationKey(botID, "qq", conloomapi.ConvPrivate, "private_"+admin.PlatformID)
	cursor := p.chat(takeover, 0).NextAfter
	p.send(takeover, cat)
	got = p.botReplies(takeover, cursor, 1)
	if got[0].Text != catReply || got[0].Channel != store.ChannelPanel {
		t.Fatalf("reply to the panel message = %+v, want %q on the panel channel", got[0], catReply)
	}
	e.requireNoAction(300 * time.Millisecond)
	tail := p.chat(takeover, cursor).Messages
	if len(tail) != 2 || tail[0].Text != cat || tail[0].Channel != store.ChannelPanel || tail[0].SenderID != admin.PlatformID {
		t.Fatalf("chat after the takeover = %+v, want the panel message as %s, then the reply", tail, admin.PlatformID)
	}
	cursor = tail[len(tail)-1].Seq

	e.say(adminChat, admin, ask, mock.SayOptions{})
	if a := e.drainOne(); a.Content.Text != answer {
		t.Fatalf("platform reply = %q, want %q", a.Content.Text, answer)
	}
	reqs := fake.Requests()
	if prompt := requestText(reqs[len(reqs)-1]); !strings.Contains(prompt, cat) || !strings.Contains(prompt, catReply) {
		t.Errorf("model request for the platform message lacks the panel turn: %q", prompt)
	}
	tail = p.chat(takeover, cursor).Messages
	if len(tail) != 2 || tail[0].Channel != "" || tail[1].Text != answer || tail[1].Channel != "" {
		t.Fatalf("chat after the platform message = %+v, want it and its reply on the platform channel", tail)
	}

	// Deleting the panel chat removes it; platform chats cannot be deleted.
	if status, raw := p.do(http.MethodDelete, convAPIPath(takeover), nil); status != http.StatusBadRequest {
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
	p.get(convAPIPath(takeover), &conv)
}
