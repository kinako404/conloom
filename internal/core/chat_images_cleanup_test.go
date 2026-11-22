package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestCleanup_PurgesUnsentChatImages(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	ctx := context.Background()
	key := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "chat_1")
	old := time.Now().Add(-25 * time.Hour)
	for _, img := range []struct{ id, messageID string }{{"img_unsent", ""}, {"img_sent", "msg_1"}} {
		if err := env.st.PutChatImage(ctx, store.ChatImage{
			ID: img.id, ConversationKey: key, MessageID: img.messageID,
			ViewJPEG: []byte{1, 2, 3}, ViewWidth: 3, ViewHeight: 2,
			ReplyJPEG: []byte{4, 5}, ReplyWidth: 3, ReplyHeight: 2, ReplyTokens: 85,
			CreatedBy: "panel:admin", CreatedAt: old,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := env.o.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "unsent chat image purged", func() bool {
		return env.counter(t, "conloom_cleanup_deleted_total", "table", "chat_images") == 1
	})
	got, err := env.st.GetChatImages(ctx, key, []string{"img_unsent", "img_sent"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["img_unsent"]; ok {
		t.Error("unsent image older than the grace period kept")
	}
	if img, ok := got["img_sent"]; !ok || img.MessageID != "msg_1" {
		t.Errorf("sent image = %+v, %v; want kept", img, ok)
	}
	if logs := env.logs.String(); !strings.Contains(logs, "purged unsent chat images") {
		t.Errorf("logs lack the purge:\n%s", logs)
	}
}
