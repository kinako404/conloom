package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// seedImageMessage stores a message from user 20001 (小明) in conv carrying
// attachments; each image ID among them is first stored as an unsent image
// of conv, which storing the message then claims.
func (h *storeHarness) seedImageMessage(conv store.ConversationRecord, text string, at time.Time, attachments []conloomapi.Attachment) store.MessageRecord {
	h.t.Helper()
	ctx := context.Background()
	for _, a := range attachments {
		if a.Type != "image" || a.ID == "" {
			continue
		}
		if err := h.db.PutChatImage(ctx, store.ChatImage{ID: a.ID, ConversationKey: conv.Key,
			ViewJPEG: []byte{1, 2, 3}, ViewWidth: 3, ViewHeight: 2, ReplyJPEG: []byte{4, 5}, ReplyWidth: 3, ReplyHeight: 2,
			CreatedAt: at}); err != nil {
			h.t.Fatalf("PutChatImage %s: %v", a.ID, err)
		}
	}
	h.seeded++
	m := store.MessageRecord{
		ID: fmt.Sprintf("msg_%d", h.seeded), BotInstanceID: conv.BotInstanceID, ConversationKey: conv.Key, Platform: conv.Platform,
		PlatformMessageID: fmt.Sprintf("panel_%d", h.seeded), ActorID: "user_20001", PlatformUserID: "20001", DisplayName: "小明",
		Text: text, Attachments: attachments, OccurredAt: at.UTC(), CreatedAt: at.UTC(), Channel: store.ChannelPanel,
	}
	h.ingestAs(conv.BotInstanceID, store.Ingest{Message: &m})
	got, err := h.db.GetMessage(ctx, conv.BotInstanceID, m.ID)
	if err != nil {
		h.t.Fatalf("read seeded message: %v", err)
	}
	return got
}

// rawMessages decodes the messages array of body as raw JSON objects.
func rawMessages(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var resp struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return resp.Messages
}

func TestChat_PostMessageWithImages(t *testing.T) {
	h, chat := newChatHarness(t)
	private := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	path := convPath(private.Key, "messages")

	w := h.write(http.MethodPost, path, `{"text":"","images":["img_a","img_b"]}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %s", w.Code, w.Body)
	}
	if submitted, _, _ := chat.calls(); len(submitted) != 1 || submitted[0] != private.Key+"|"+testUser+"||img_a,img_b" {
		t.Fatalf("submitted = %q, want the text-less message with img_a and img_b", submitted)
	}
	events := h.audits("panel.message")
	checkChatAudit(t, events, private.Key)
	var detail map[string]any
	if err := json.Unmarshal([]byte(events[0].Detail), &detail); err != nil {
		t.Fatalf("audit detail %s: %v", events[0].Detail, err)
	}
	if detail["images"] != float64(2) || detail["message_id"] != "msg_panel_1" {
		t.Errorf("audit detail %s, want message_id msg_panel_1 and images 2", events[0].Detail)
	}

	if w := h.write(http.MethodPost, path, map[string]string{"text": "只有字"}); w.Code != http.StatusAccepted {
		t.Fatalf("text-only: status %d, body %s", w.Code, w.Body)
	}
	if submitted, _, _ := chat.calls(); len(submitted) != 2 || submitted[1] != private.Key+"|"+testUser+"|只有字" {
		t.Fatalf("submitted = %q, want the text-only message without images", submitted)
	}
	events = h.audits("panel.message")
	for _, e := range events {
		if e.Detail == `{"message_id":"msg_panel_2"}` {
			return
		}
	}
	t.Errorf("audits = %+v, want the text-only one's detail exactly {\"message_id\":\"msg_panel_2\"}", events)
}

func TestChat_PostMessageBadImages(t *testing.T) {
	h, chat := newChatHarness(t)
	private := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	chat.setSubmitErr(core.ErrBadImages)

	body := errorBody(t, h.write(http.MethodPost, convPath(private.Key, "messages"), `{"text":"看","images":["img_x"]}`),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if want := strings.TrimPrefix(core.ErrBadImages.Error(), "core: "); body.Message != want {
		t.Errorf("message %q, want %q", body.Message, want)
	}
	if n := len(h.audits("panel.message")); n != 0 {
		t.Errorf("panel.message audits = %d, want none", n)
	}
}

func TestChat_PollShowsImages(t *testing.T) {
	h, _ := newChatHarness(t)
	private := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	at := time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)
	withImages := h.seedImageMessage(private, "看这个", at,
		[]conloomapi.Attachment{{Type: "image", ID: "img_a"}, {Type: "image"}})
	plain := h.seedPanelMessage(private, "只有字", at.Add(time.Minute))

	for _, path := range []string{convPath(private.Key, "chat"), convPath(private.Key, "messages")} {
		msgs := rawMessages(t, h.getJSON(path, nil))
		byID := map[string]map[string]json.RawMessage{}
		for _, m := range msgs {
			var id string
			if err := json.Unmarshal(m["id"], &id); err != nil {
				t.Fatal(err)
			}
			byID[id] = m
		}
		var images []string
		if err := json.Unmarshal(byID[withImages.ID]["images"], &images); err != nil || !slices.Equal(images, []string{"img_a"}) {
			t.Errorf("%s: images of %s = %s (%v), want [img_a]", path, withImages.ID, byID[withImages.ID]["images"], err)
		}
		if raw, ok := byID[plain.ID]["images"]; ok {
			t.Errorf("%s: text-only message has images %s, want no images key", path, raw)
		}
	}
}

func TestChats_LastShowsImages(t *testing.T) {
	h := newBotsHarness(t)
	base := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	chat := h.panelChat(dataBot, "小明")
	h.seedImageMessage(chat, "", base, []conloomapi.Attachment{{Type: "image", ID: "img_a"}})
	plain := h.panelChat(dataBot, "小红")
	h.seedImageMessage(plain, "在吗", base.Add(-time.Minute), nil)

	var resp struct {
		Bots []struct {
			Chats []struct {
				Key  string                     `json:"key"`
				Last map[string]json.RawMessage `json:"last"`
			} `json:"chats"`
		} `json:"bots"`
	}
	h.getJSON("/panel/api/chats", &resp)
	found := 0
	for _, b := range resp.Bots {
		for _, c := range b.Chats {
			switch c.Key {
			case chat.Key:
				found++
				var images []string
				if err := json.Unmarshal(c.Last["images"], &images); err != nil || !slices.Equal(images, []string{"img_a"}) {
					t.Errorf("last.images = %s (%v), want [img_a]", c.Last["images"], err)
				}
			case plain.Key:
				found++
				if raw, ok := c.Last["images"]; ok {
					t.Errorf("text-only last has images %s, want no images key", raw)
				}
			}
		}
	}
	if found != 2 {
		t.Fatalf("chats = %+v, want %s and %s", resp, chat.Key, plain.Key)
	}
}
