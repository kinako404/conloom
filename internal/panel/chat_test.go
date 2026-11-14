package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// fakeChat is a ChatService over the harness's store: SubmitPanel records
// the call and answers submitErr, CreatePanelChat stores a panel
// conversation of a configured bot, DeletePanelChat deletes panel
// conversations only, and Busy answers busy.
type fakeChat struct {
	h *storeHarness

	mu        sync.Mutex
	submitErr error
	submitted []string // convKey|panelUser|text
	created   []string // botID|panelUser|displayName
	deleted   []string
	busy      map[string]bool
}

func (f *fakeChat) SubmitPanel(_ context.Context, convKey, panelUser, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submitErr != nil {
		return "", f.submitErr
	}
	f.submitted = append(f.submitted, convKey+"|"+panelUser+"|"+text)
	return fmt.Sprintf("msg_panel_%d", len(f.submitted)), nil
}

func (f *fakeChat) CreatePanelChat(_ context.Context, botID, panelUser, displayName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if botID != dataBot {
		return "", store.ErrNotFound
	}
	f.created = append(f.created, botID+"|"+panelUser+"|"+displayName)
	id := fmt.Sprintf("panel_%d", len(f.created))
	conv := store.ConversationRecord{
		BotInstanceID: dataBot, ID: id, Key: store.ConversationKey(dataBot, store.PlatformPanel, conloomapi.ConvPrivate, id),
		Type: conloomapi.ConvPrivate, Platform: store.PlatformPanel, PlatformConversationID: id,
	}
	f.h.storeConv(conv)
	return conv.Key, nil
}

func (f *fakeChat) DeletePanelChat(ctx context.Context, convKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	conv, err := f.h.db.GetConversationByKey(ctx, convKey)
	if err != nil {
		return err
	}
	if conv.Platform != store.PlatformPanel {
		return core.ErrNotPanelChat
	}
	f.deleted = append(f.deleted, convKey)
	return f.h.db.DeleteConversation(ctx, convKey)
}

func (f *fakeChat) Busy(convKey string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy[convKey]
}

func (f *fakeChat) setBusy(convKey string, busy bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy[convKey] = busy
}

func (f *fakeChat) setSubmitErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitErr = err
}

func (f *fakeChat) calls() (submitted, created, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.submitted...), append([]string(nil), f.created...), append([]string(nil), f.deleted...)
}

// newChatHarness is a writeHarness whose panel chats are fakeChat.
func newChatHarness(t *testing.T) (*writeHarness, *fakeChat) {
	t.Helper()
	chat := &fakeChat{busy: map[string]bool{}}
	h := newWriteHarness(t, func(d *Deps) { d.Chat = chat })
	chat.h = h.storeHarness
	return h, chat
}

type chatMsgResp struct {
	msgResp
	Channel string `json:"channel"`
}

type chatResp struct {
	Messages  []chatMsgResp `json:"messages"`
	Typing    bool          `json:"typing"`
	NextAfter int64         `json:"next_after"`
}

func (h *writeHarness) chat(key string, after int64) chatResp {
	h.t.Helper()
	var resp chatResp
	h.getJSON(fmt.Sprintf("%s?after=%d", convPath(key, "chat"), after), &resp)
	return resp
}

func chatIDs(msgs []chatMsgResp) []string {
	out := []string{}
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

// seedPanelMessage stores a message written in the panel in conv.
func (h *storeHarness) seedPanelMessage(conv store.ConversationRecord, text string, at time.Time) store.MessageRecord {
	h.t.Helper()
	h.seeded++
	m := store.MessageRecord{
		ID: fmt.Sprintf("msg_%d", h.seeded), BotInstanceID: dataBot, ConversationKey: conv.Key, Platform: conv.Platform,
		PlatformMessageID: fmt.Sprintf("panel_%d", h.seeded), ActorID: "user_20001", PlatformUserID: "20001", DisplayName: "小明",
		Text: text, OccurredAt: at.UTC(), CreatedAt: at.UTC(), Channel: store.ChannelPanel,
	}
	h.ingest(h.seeded, store.Ingest{Message: &m})
	got, err := h.db.GetMessage(context.Background(), dataBot, m.ID)
	if err != nil {
		h.t.Fatalf("read seeded message: %v", err)
	}
	return got
}

func checkChatAudit(t *testing.T, events []store.AuditEvent, target string, secrets ...string) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("audits = %+v, want one", events)
	}
	e := events[0]
	if e.BotInstanceID != dataBot || e.Actor != "panel:"+testUser || e.Target != target {
		t.Errorf("audit = %+v, want bot %s, actor panel:%s, target %s", e, dataBot, testUser, target)
	}
	for _, s := range secrets {
		if strings.Contains(e.Detail, s) {
			t.Errorf("audit detail %s contains %q", e.Detail, s)
		}
	}
}

func TestChat_PostMessage(t *testing.T) {
	h, chat := newChatHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	path := convPath(private.Key, "messages")

	w := h.write(http.MethodPost, path, map[string]string{"text": "今晚吃什么"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %s", w.Code, w.Body)
	}
	var resp struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.MessageID != "msg_panel_1" {
		t.Fatalf("body %s (%v), want message_id msg_panel_1", w.Body, err)
	}
	if submitted, _, _ := chat.calls(); len(submitted) != 1 || submitted[0] != private.Key+"|"+testUser+"|今晚吃什么" {
		t.Fatalf("submitted = %q", submitted)
	}
	checkChatAudit(t, h.audits("panel.message"), private.Key, "今晚吃什么")
	if !strings.Contains(h.audits("panel.message")[0].Detail, "msg_panel_1") {
		t.Errorf("audit detail %s lacks the message ID", h.audits("panel.message")[0].Detail)
	}

	errorBody(t, h.do(http.MethodPost, path, `{"text":"x"}`, withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.write(http.MethodPost, convPath("qq-main:qq:private:missing", "messages"), map[string]string{"text": "x"}),
		http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPost, path, `{"text":"x","extra":1}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if submitted, _, _ := chat.calls(); len(submitted) != 1 {
		t.Fatalf("rejected requests reached the chat service: %q", submitted)
	}

	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{core.ErrNotPrivate, http.StatusBadRequest, conloomapi.CodeInvalidEvent},
		{core.ErrBadText, http.StatusBadRequest, conloomapi.CodeInvalidEvent},
		{fmt.Errorf("submit: %w", core.ErrNoUserYet), http.StatusBadRequest, conloomapi.CodeInvalidEvent},
		{core.ErrNotPanelChat, http.StatusBadRequest, conloomapi.CodeInvalidEvent},
		{store.ErrNotFound, http.StatusNotFound, conloomapi.CodeNotFound},
		{errors.New("disk full"), http.StatusInternalServerError, conloomapi.CodeInternal},
	} {
		chat.setSubmitErr(tc.err)
		body := errorBody(t, h.write(http.MethodPost, path, map[string]string{"text": "x"}), tc.status, tc.code)
		if tc.status == http.StatusBadRequest && strings.Contains(body.Message, "core:") {
			t.Errorf("%v: message %q shows the package prefix", tc.err, body.Message)
		}
	}
	if n := len(h.audits("panel.message")); n != 1 {
		t.Fatalf("panel.message audits = %d, want only the accepted message's", n)
	}
}

func TestChat_PollAfter(t *testing.T) {
	h, _ := newChatHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	other := h.seedConv("qq", conloomapi.ConvPrivate, "20002")
	at := time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)
	m1 := h.seedMessage(private, "在吗", at, false)
	h.seedMessage(other, "别的会话", at, false)
	m2 := h.seedPanelMessage(private, "从面板发的", at.Add(time.Minute))
	m3 := h.seedMessage(private, "在的", at.Add(2*time.Minute), true)

	all := h.chat(private.Key, 0)
	if ids := chatIDs(all.Messages); strings.Join(ids, ",") != strings.Join([]string{m1.ID, m2.ID, m3.ID}, ",") {
		t.Fatalf("messages = %v, want oldest first %s %s %s", ids, m1.ID, m2.ID, m3.ID)
	}
	if all.NextAfter != m3.Seq || all.Typing {
		t.Fatalf("next_after %d typing %v, want %d false", all.NextAfter, all.Typing, m3.Seq)
	}
	if ch := []string{all.Messages[0].Channel, all.Messages[1].Channel, all.Messages[2].Channel}; ch[0] != "" || ch[1] != "panel" || ch[2] != "" {
		t.Fatalf("channels = %q", ch)
	}
	if m := all.Messages[2]; !m.FromBot || m.Text != "在的" || m.Seq != m3.Seq {
		t.Fatalf("bot message = %+v", m)
	}

	if got := h.chat(private.Key, m1.Seq); strings.Join(chatIDs(got.Messages), ",") != m2.ID+","+m3.ID || got.NextAfter != m3.Seq {
		t.Fatalf("after %d: %v next %d", m1.Seq, chatIDs(got.Messages), got.NextAfter)
	}
	if got := h.chat(private.Key, m3.Seq); len(got.Messages) != 0 || got.NextAfter != m3.Seq {
		t.Fatalf("caught up: %v next %d, want none and %d", chatIDs(got.Messages), got.NextAfter, m3.Seq)
	}
	if body := string(h.getJSON(convPath(private.Key, "chat")+"?after="+fmt.Sprint(m3.Seq), nil)); !strings.Contains(body, `"messages":[]`) {
		t.Errorf("caught up body %s, want an empty messages array", body)
	}

	var browse struct {
		Messages []chatMsgResp `json:"messages"`
	}
	h.getJSON(convPath(private.Key, "messages"), &browse)
	if len(browse.Messages) != 3 || browse.Messages[1].Channel != "panel" || browse.Messages[0].Channel != "" {
		t.Errorf("browse = %+v, want the channel of each message", browse.Messages)
	}

	h.getErr(convPath(private.Key, "chat")+"?after=-1", http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	h.getErr(convPath(private.Key, "chat")+"?after=x", http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	h.getErr(convPath("qq-main:qq:private:missing", "chat"), http.StatusNotFound, conloomapi.CodeNotFound)
}

func TestChat_PollAtMost200(t *testing.T) {
	h, _ := newChatHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	at := time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)
	var last store.MessageRecord
	for i := range 201 {
		last = h.seedMessage(private, fmt.Sprintf("第 %d 条", i), at.Add(time.Duration(i)*time.Second), false)
	}
	first := h.chat(private.Key, 0)
	if len(first.Messages) != 200 || first.NextAfter != first.Messages[199].Seq {
		t.Fatalf("first page: %d messages, next_after %d", len(first.Messages), first.NextAfter)
	}
	rest := h.chat(private.Key, first.NextAfter)
	if len(rest.Messages) != 1 || rest.Messages[0].ID != last.ID || rest.NextAfter != last.Seq {
		t.Fatalf("second page: %v next_after %d, want [%s] %d", chatIDs(rest.Messages), rest.NextAfter, last.ID, last.Seq)
	}
}

func TestChat_Typing(t *testing.T) {
	h, chat := newChatHarness(t)
	ctx := context.Background()
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	at := time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)

	if h.chat(private.Key, 0).Typing {
		t.Fatal("typing with nothing running")
	}
	chat.setBusy(private.Key, true)
	if !h.chat(private.Key, 0).Typing {
		t.Fatal("not typing while the conversation has tasks")
	}
	chat.setBusy(private.Key, false)

	h.seedTask(private, "task_01", conloomapi.TaskRunning, conloomapi.TaskMeta{}, at)
	action := func(id, channel string) store.OutboxRecord {
		return store.OutboxRecord{
			Action: conloomapi.Action{ActionID: id, Type: conloomapi.ActionMessageSend, ConversationID: private.ID,
				Content: conloomapi.ActionContent{Text: "在的"}, CreatedAt: at},
			BotInstanceID: dataBot, ConversationKey: private.Key, Platform: "qq", CreatedAt: at,
			ExpiresAt: at.Add(time.Hour), NotBefore: at.Add(time.Minute), Channel: channel,
		}
	}
	if err := h.db.CompleteTask(ctx, store.TaskCompletion{TaskID: "task_01", Status: conloomapi.TaskSucceeded,
		Actions: []store.OutboxRecord{action("act_platform", "")}}); err != nil {
		t.Fatal(err)
	}
	if h.chat(private.Key, 0).Typing {
		t.Fatal("typing for a pending platform-channel action")
	}

	h.seedTask(private, "task_02", conloomapi.TaskRunning, conloomapi.TaskMeta{}, at)
	if err := h.db.CompleteTask(ctx, store.TaskCompletion{TaskID: "task_02", Status: conloomapi.TaskSucceeded,
		Actions: []store.OutboxRecord{action("act_panel", store.ChannelPanel)}}); err != nil {
		t.Fatal(err)
	}
	if !h.chat(private.Key, 0).Typing {
		t.Fatal("not typing while a panel part is still to be delivered")
	}
}

func TestChat_CreateAndDelete(t *testing.T) {
	h, chat := newChatHarness(t)

	w := h.write(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": dataBot, "display_name": "小明"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", w.Code, w.Body)
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Key == "" {
		t.Fatalf("create body %s (%v)", w.Body, err)
	}
	if _, c, _ := chat.calls(); len(c) != 1 || c[0] != dataBot+"|"+testUser+"|小明" {
		t.Fatalf("created = %q", c)
	}
	checkChatAudit(t, h.audits("panel.chat.create"), created.Key, "小明")
	var conv convResp
	h.getJSON(convPath(created.Key), &conv)
	if conv.Platform != store.PlatformPanel || conv.Type != conloomapi.ConvPrivate {
		t.Fatalf("created conversation = %+v", conv)
	}

	errorBody(t, h.write(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": "tg-main", "display_name": "小明"}),
		http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPost, "/panel/api/chats", `{"bot":"qq-main"}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.do(http.MethodPost, "/panel/api/chats", `{"bot_instance_id":"qq-main"}`, withCookie(h.cookie)),
		http.StatusForbidden, conloomapi.CodeForbidden)
	if _, c, _ := chat.calls(); len(c) != 1 {
		t.Fatalf("rejected creates: %q", c)
	}
	if n := len(h.audits("panel.chat.create")); n != 1 {
		t.Fatalf("panel.chat.create audits = %d, want 1", n)
	}

	panelConv, err := h.db.GetConversationByKey(context.Background(), created.Key)
	if err != nil {
		t.Fatal(err)
	}
	h.seedPanelMessage(panelConv, "删掉我", time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC))

	errorBody(t, h.do(http.MethodDelete, convPath(created.Key), "", withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.write(http.MethodDelete, convPath(writeGroup.Key), nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	h.getJSON(convPath(writeGroup.Key), nil)
	errorBody(t, h.write(http.MethodDelete, convPath("qq-main:panel:private:missing"), nil), http.StatusNotFound, conloomapi.CodeNotFound)

	if w := h.write(http.MethodDelete, convPath(created.Key), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d, body %s", w.Code, w.Body)
	}
	h.getErr(convPath(created.Key), http.StatusNotFound, conloomapi.CodeNotFound)
	if _, _, d := chat.calls(); len(d) != 1 || d[0] != created.Key {
		t.Fatalf("deleted = %q", d)
	}
	checkChatAudit(t, h.audits("panel.chat.delete"), created.Key, "删掉我")
}

func TestChat_ConversationsListBots(t *testing.T) {
	h, _ := newChatHarness(t)
	var list struct {
		Bots []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Platform string `json:"platform"`
		} `json:"bots"`
	}
	h.getJSON("/panel/api/conversations", &list)
	if len(list.Bots) != 1 || list.Bots[0].ID != dataBot || list.Bots[0].Name != "织语" || list.Bots[0].Platform != "qq" {
		t.Fatalf("bots = %+v, want the configured bot", list.Bots)
	}
}

func TestChat_Unavailable(t *testing.T) {
	h := newWriteHarness(t, nil)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	errorBody(t, h.write(http.MethodPost, convPath(private.Key, "messages"), map[string]string{"text": "x"}),
		http.StatusServiceUnavailable, conloomapi.CodeInternal)
	errorBody(t, h.write(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": dataBot}),
		http.StatusServiceUnavailable, conloomapi.CodeInternal)
	errorBody(t, h.write(http.MethodDelete, convPath(private.Key), nil), http.StatusServiceUnavailable, conloomapi.CodeInternal)
	h.getErr(convPath(private.Key, "chat"), http.StatusServiceUnavailable, conloomapi.CodeInternal)
}
