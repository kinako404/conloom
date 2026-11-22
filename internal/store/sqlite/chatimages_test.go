package sqlite

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func chatImageKey(convID string) string {
	return store.ConversationKey(panelBotA, store.PlatformPanel, conloomapi.ConvPrivate, convID)
}

// testChatImage is an image of convKey with fake bytes, uploaded at at.
func testChatImage(convKey, id string, at time.Time) store.ChatImage {
	return store.ChatImage{
		ID: id, ConversationKey: convKey,
		ViewJPEG: []byte{1, 2, 3}, ViewWidth: 1536, ViewHeight: 1024,
		ReplyJPEG: []byte{4, 5}, ReplyWidth: 768, ReplyHeight: 512, ReplyTokens: 520,
		CreatedBy: "panel:admin", CreatedAt: at,
	}
}

// seedChatImage stores an unsent image id of convKey uploaded at at.
func seedChatImage(t *testing.T, db *DB, convKey, id string, at time.Time) {
	t.Helper()
	if err := db.PutChatImage(context.Background(), testChatImage(convKey, id, at)); err != nil {
		t.Fatalf("PutChatImage %s: %v", id, err)
	}
}

func imageAttachments(ids ...string) []conloomapi.Attachment {
	out := []conloomapi.Attachment{}
	for _, id := range ids {
		out = append(out, conloomapi.Attachment{Type: "image", ID: id})
	}
	return out
}

// ingestChatMessage stores message msgID of the panel bot's chat convID on
// platform, from the bot when fromBot, with attachments, deleting messages
// first when del is set.
func ingestChatMessage(db *DB, platform, convID, msgID string, fromBot bool, attachments []conloomapi.Attachment,
	del *store.MessageDelete) error {
	now := time.Now().UTC()
	key := store.ConversationKey(panelBotA, platform, conloomapi.ConvPrivate, convID)
	uid, actor := "admin", "panel_admin"
	if fromBot {
		uid, actor = "", ""
	}
	_, err := db.Ingest(context.Background(), store.Ingest{
		Event: store.EventRecord{
			BotInstanceID: panelBotA, IdempotencyKey: "k_" + msgID, RequestHash: "h_" + msgID,
			EventID: "evt_" + msgID, Response: []byte(`{}`), RawEvent: []byte(`{}`), CreatedAt: now,
		},
		Conversation: &store.ConversationRecord{
			BotInstanceID: panelBotA, ID: convID, Key: key, Type: conloomapi.ConvPrivate,
			Platform: platform, PlatformConversationID: convID,
		},
		Message: &store.MessageRecord{
			ID: msgID, BotInstanceID: panelBotA, ConversationKey: key, Platform: platform,
			PlatformMessageID: "p_" + msgID, ActorID: actor, PlatformUserID: uid, DisplayName: "小明",
			Attachments: attachments, FromBot: fromBot, OccurredAt: now, CreatedAt: now,
		},
		DeleteMessages: del,
	})
	return err
}

func mustIngestChatMessage(t *testing.T, db *DB, platform, convID, msgID string, fromBot bool,
	attachments []conloomapi.Attachment, del *store.MessageDelete) store.MessageRecord {
	t.Helper()
	if err := ingestChatMessage(db, platform, convID, msgID, fromBot, attachments, del); err != nil {
		t.Fatalf("Ingest %s: %v", msgID, err)
	}
	m, err := db.GetMessage(context.Background(), panelBotA, msgID)
	if err != nil {
		t.Fatalf("GetMessage %s: %v", msgID, err)
	}
	return m
}

// chatImageIDs lists the IDs of convKey's images, sorted.
func chatImageIDs(t *testing.T, db *DB, convKey string) []string {
	t.Helper()
	rows, err := db.r.Query(`SELECT id FROM chat_images WHERE conversation_key = ? ORDER BY id`, convKey)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func getChatImages(t *testing.T, db *DB, convKey string, ids []string, data store.ChatImageData) map[string]store.ChatImage {
	t.Helper()
	got, err := db.GetChatImages(context.Background(), convKey, ids, data)
	if err != nil {
		t.Fatalf("GetChatImages: %v", err)
	}
	if got == nil {
		t.Fatal("GetChatImages returned a nil map")
	}
	return got
}

func TestMigration0023_OnlyAddsChatImages(t *testing.T) {
	_, added, _ := migrateOnlyAdding(t, 23)
	want := []string{"chat_images", "chat_images_conversation_message", "chat_images_unsent", "sqlite_autoindex_chat_images_1"}
	if !slices.Equal(added, want) {
		t.Fatalf("added %v, want %v", added, want)
	}
}

func TestChatImages_PutGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	k1, k2 := chatImageKey("chat_1"), chatImageKey("chat_2")
	at := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	a := testChatImage(k1, "img_a", at)
	b := testChatImage(k1, "img_b", at.Add(time.Minute))
	b.ViewJPEG, b.ReplyJPEG, b.ReplyTokens = []byte{6, 7}, []byte{8}, 85
	for _, img := range []store.ChatImage{a, b} {
		if err := db.PutChatImage(ctx, img); err != nil {
			t.Fatalf("PutChatImage %s: %v", img.ID, err)
		}
	}
	if err := db.SetChatImageDescription(ctx, k1, "img_b", "一只橘猫"); err != nil {
		t.Fatal(err)
	}
	b.Description = "一只橘猫"

	meta := func(img store.ChatImage) store.ChatImage {
		img.ViewJPEG, img.ReplyJPEG = nil, nil
		return img
	}
	check := func(name string, got map[string]store.ChatImage, want ...store.ChatImage) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %d images %v, want %d", name, len(got), got, len(want))
		}
		for _, w := range want {
			g, ok := got[w.ID]
			if !ok {
				t.Fatalf("%s: %s missing", name, w.ID)
			}
			if !g.CreatedAt.Equal(w.CreatedAt) {
				t.Errorf("%s: %s CreatedAt = %v, want %v", name, w.ID, g.CreatedAt, w.CreatedAt)
			}
			g.CreatedAt, w.CreatedAt = time.Time{}, time.Time{}
			if !slices.Equal(g.ViewJPEG, w.ViewJPEG) || !slices.Equal(g.ReplyJPEG, w.ReplyJPEG) ||
				(g.ViewJPEG == nil) != (w.ViewJPEG == nil) || (g.ReplyJPEG == nil) != (w.ReplyJPEG == nil) {
				t.Errorf("%s: %s blobs view %v reply %v, want %v %v", name, w.ID, g.ViewJPEG, g.ReplyJPEG, w.ViewJPEG, w.ReplyJPEG)
			}
			g.ViewJPEG, g.ReplyJPEG, w.ViewJPEG, w.ReplyJPEG = nil, nil, nil, nil
			if !reflect.DeepEqual(g, w) {
				t.Errorf("%s: %s = %+v, want %+v", name, w.ID, g, w)
			}
		}
	}

	ids := []string{"img_a", "img_b", "img_x"}
	check("metadata", getChatImages(t, db, k1, ids, 0), meta(a), meta(b))
	ra, rb := meta(a), meta(b)
	ra.ReplyJPEG, rb.ReplyJPEG = a.ReplyJPEG, b.ReplyJPEG
	check("reply", getChatImages(t, db, k1, ids, store.ChatImageReply), ra, rb)
	va, vb := meta(a), meta(b)
	va.ViewJPEG, vb.ViewJPEG = a.ViewJPEG, b.ViewJPEG
	check("view", getChatImages(t, db, k1, ids, store.ChatImageView), va, vb)
	check("both", getChatImages(t, db, k1, ids, store.ChatImageReply|store.ChatImageView), a, b)
	check("other chat", getChatImages(t, db, k2, ids, store.ChatImageReply))
	check("no ids", getChatImages(t, db, k1, nil, store.ChatImageReply))
}

func TestChatImages_ClaimedByStoredMessage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	k1, k2 := chatImageKey("chat_1"), chatImageKey("chat_2")
	qq := store.ConversationKey(panelBotA, "qq", conloomapi.ConvPrivate, "20001")
	now := time.Now()
	seedChatImage(t, db, k1, "img_a", now)
	seedChatImage(t, db, k1, "img_b", now)
	seedChatImage(t, db, k1, "img_c", now)
	seedChatImage(t, db, qq, "img_q", now)

	msg := mustIngestChatMessage(t, db, store.PlatformPanel, "chat_1", "m1", false, imageAttachments("img_a", "img_b"), nil)
	got := getChatImages(t, db, k1, []string{"img_a", "img_b", "img_c"}, 0)
	for id, want := range map[string]string{"img_a": msg.ID, "img_b": msg.ID, "img_c": ""} {
		if got[id].MessageID != want {
			t.Errorf("%s MessageID = %q, want %q", id, got[id].MessageID, want)
		}
	}
	if !slices.Equal(msg.Attachments, imageAttachments("img_a", "img_b")) {
		t.Errorf("stored attachments = %+v", msg.Attachments)
	}

	for _, c := range []struct {
		name, convID, msgID string
		ids                 []string
	}{
		{"already sent", "chat_1", "m2", []string{"img_c", "img_a"}},
		{"duplicate", "chat_1", "m3", []string{"img_c", "img_c"}},
		{"unknown", "chat_1", "m4", []string{"img_x"}},
		{"other chat", "chat_2", "m5", []string{"img_c"}},
	} {
		err := ingestChatMessage(db, store.PlatformPanel, c.convID, c.msgID, false, imageAttachments(c.ids...), nil)
		if err == nil {
			t.Errorf("%s: claiming %v succeeded", c.name, c.ids)
		}
		if _, err := db.GetMessage(ctx, panelBotA, c.msgID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: message stored after a failed claim, err = %v", c.name, err)
		}
		if _, err := db.GetEvent(ctx, panelBotA, "k_"+c.msgID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: event stored after a failed claim, err = %v", c.name, err)
		}
	}
	if got := getChatImages(t, db, k1, []string{"img_c"}, 0); got["img_c"].MessageID != "" {
		t.Errorf("img_c claimed by a failed ingest: %+v", got["img_c"])
	}
	if ids := chatImageIDs(t, db, k2); len(ids) != 0 {
		t.Errorf("images of %s = %v", k2, ids)
	}

	mustIngestChatMessage(t, db, "qq", "20001", "mq", false, imageAttachments("img_q"), nil)
	if got := getChatImages(t, db, qq, []string{"img_q"}, 0); got["img_q"].MessageID != "" {
		t.Errorf("a QQ message claimed img_q: %+v", got["img_q"])
	}

	plain := mustIngestChatMessage(t, db, store.PlatformPanel, "chat_1", "m6", false,
		[]conloomapi.Attachment{{Type: "image", URL: "https://example.invalid/a.png"}, {Type: "file", ID: "img_c"}}, nil)
	if len(plain.Attachments) != 2 {
		t.Fatalf("attachments = %+v", plain.Attachments)
	}
	if got := getChatImages(t, db, k1, []string{"img_c"}, 0); got["img_c"].MessageID != "" {
		t.Errorf("an attachment without an image ID claimed img_c: %+v", got["img_c"])
	}
}

func TestChatImages_WithdrawAndDeleteRemoveThem(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	createPanelBot(t, db, store.PanelBot{ID: panelBotA, Name: "小明", PersonaTemplate: "default", CreatedBy: "admin"})
	k1, k2 := chatImageKey("chat_1"), chatImageKey("chat_2")
	now := time.Now()
	for _, id := range []string{"img_a", "img_b", "img_c"} {
		seedChatImage(t, db, k1, id, now)
	}
	seedChatImage(t, db, k2, "img_k2", now)
	m1 := mustIngestChatMessage(t, db, store.PlatformPanel, "chat_1", "m1", false, imageAttachments("img_a"), nil)
	m2 := mustIngestChatMessage(t, db, store.PlatformPanel, "chat_1", "m2", false, imageAttachments("img_b"), nil)
	m3 := mustIngestChatMessage(t, db, store.PlatformPanel, "chat_1", "m3", true, nil, nil)
	if !(m1.Seq < m2.Seq && m2.Seq < m3.Seq) {
		t.Fatalf("seqs %d %d %d", m1.Seq, m2.Seq, m3.Seq)
	}

	mustIngestChatMessage(t, db, store.PlatformPanel, "chat_1", "m4", true, nil,
		&store.MessageDelete{ConversationKey: k1, FromSeq: m3.Seq})
	if ids := chatImageIDs(t, db, k1); !slices.Equal(ids, []string{"img_a", "img_b", "img_c"}) {
		t.Fatalf("images after regenerating the reply = %v, want all kept", ids)
	}

	if n, err := db.DeleteMessagesFrom(ctx, k1, m2.Seq); err != nil || n != 2 {
		t.Fatalf("DeleteMessagesFrom = %d, %v; want 2", n, err)
	}
	if ids := chatImageIDs(t, db, k1); !slices.Equal(ids, []string{"img_a", "img_c"}) {
		t.Fatalf("images after withdrawing m2 = %v, want [img_a img_c]", ids)
	}

	if err := db.DeleteConversation(ctx, k1); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if ids := chatImageIDs(t, db, k1); len(ids) != 0 {
		t.Fatalf("images after deleting the chat = %v", ids)
	}
	if ids := chatImageIDs(t, db, k2); !slices.Equal(ids, []string{"img_k2"}) {
		t.Fatalf("images of the other chat = %v", ids)
	}

	mustIngestChatMessage(t, db, store.PlatformPanel, "chat_2", "m5", false, nil, nil)
	if err := db.DeleteConversation(ctx, k2); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if err := db.DeletePanelBot(ctx, panelBotA); err != nil {
		t.Fatalf("DeletePanelBot: %v", err)
	}
	if n := countRows(t, db, "chat_images"); n != 0 {
		t.Fatalf("chat_images rows = %d, want 0", n)
	}
}

func TestChatImages_PurgeOnlyUnsent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	k1 := chatImageKey("chat_1")
	now := time.Now()
	seedChatImage(t, db, k1, "img_old", now.Add(-25*time.Hour))
	seedChatImage(t, db, k1, "img_new", now.Add(-time.Hour))
	seedChatImage(t, db, k1, "img_sent", now.Add(-25*time.Hour))
	mustIngestChatMessage(t, db, store.PlatformPanel, "chat_1", "m1", false, imageAttachments("img_sent"), nil)

	n, err := db.PurgeChatImages(ctx, now.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PurgeChatImages = %d, %v; want 1", n, err)
	}
	if ids := chatImageIDs(t, db, k1); !slices.Equal(ids, []string{"img_new", "img_sent"}) {
		t.Fatalf("images left = %v, want [img_new img_sent]", ids)
	}
}

func TestChatImages_Description(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	k1, k2 := chatImageKey("chat_1"), chatImageKey("chat_2")
	seedChatImage(t, db, k1, "img_a", time.Now())

	if got := getChatImages(t, db, k1, []string{"img_a"}, 0); got["img_a"].Description != "" {
		t.Fatalf("new image description = %q", got["img_a"].Description)
	}
	if err := db.SetChatImageDescription(ctx, k1, "img_a", "一只橘猫"); err != nil {
		t.Fatalf("SetChatImageDescription: %v", err)
	}
	if got := getChatImages(t, db, k1, []string{"img_a"}, 0); got["img_a"].Description != "一只橘猫" {
		t.Fatalf("description = %q, want 一只橘猫", got["img_a"].Description)
	}
	if err := db.SetChatImageDescription(ctx, k2, "img_a", "一只橘猫"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetChatImageDescription in another chat err = %v, want ErrNotFound", err)
	}
	if err := db.SetChatImageDescription(ctx, k1, "img_x", "一只橘猫"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetChatImageDescription of an unknown image err = %v, want ErrNotFound", err)
	}
}
