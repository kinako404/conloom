package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var testConvKey = store.ConversationKey("bot_test", "qq", conloomapi.ConvGroup, "conv_10001")

func intPtr(v int) *int { return &v }

func newTestStore(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func getMember(t *testing.T, st store.Store, convKey, userID string) store.MemberMemory {
	t.Helper()
	got, err := st.GetMemberMemories(context.Background(), convKey, []string{userID})
	if err != nil {
		t.Fatalf("GetMemberMemories: %v", err)
	}
	m, ok := got[userID]
	if !ok {
		t.Fatalf("member %s has no memory row", userID)
	}
	return m
}

func putMember(t *testing.T, st store.Store, m store.MemberMemory) {
	t.Helper()
	conflicts, err := st.ApplyMemoryWrite(context.Background(),
		store.MemoryWrite{ConversationKey: m.ConversationKey, Members: []store.MemberMemory{m}})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("ApplyMemoryWrite = %v, %v", conflicts, err)
	}
}
