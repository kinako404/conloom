package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func summaryRevisions(t *testing.T, db *DB, convKey string) []store.MemoryRevision {
	t.Helper()
	revs, err := db.ListMemoryRevisions(context.Background(), convKey, "")
	if err != nil {
		t.Fatal(err)
	}
	return revs
}

func TestApplyMemoryWrite_SummaryGuard(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	write := func(w store.MemoryWrite) {
		t.Helper()
		w.ConversationKey = testConvKey
		if conflicts, err := db.ApplyMemoryWrite(ctx, w); err != nil || len(conflicts) != 0 {
			t.Fatalf("ApplyMemoryWrite = %v, %v", conflicts, err)
		}
	}
	summaryRev := func(old, next string) []store.MemoryRevision {
		return []store.MemoryRevision{{ConversationKey: testConvKey, Field: store.FieldSummary, OldValue: old, NewValue: next,
			Source: store.RevisionConsolidate}}
	}
	group := func() store.GroupMemory {
		t.Helper()
		g, err := db.GetGroupMemory(ctx, testConvKey)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}

	write(store.MemoryWrite{
		Group:   &store.GroupMemory{ConversationKey: testConvKey, Summary: "旧", CursorSeq: 1},
		Members: []store.MemberMemory{{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢猫"}},
	})
	write(store.MemoryWrite{
		Group:         &store.GroupMemory{ConversationKey: testConvKey, Summary: "新", CursorSeq: 5},
		SummaryGuards: []store.MemberVersion{{PlatformUserID: "20001", Version: 1}, {PlatformUserID: "20002"}},
		Revisions:     summaryRev("旧", "新"),
	})
	if g := group(); g.Summary != "新" || g.CursorSeq != 5 || len(summaryRevisions(t, db, testConvKey)) != 1 {
		t.Fatalf("guards hold: group %+v, want summary 新 at cursor 5 with its revision", g)
	}

	if err := db.ForgetMember(ctx, testConvKey, "20001", time.Now()); err != nil {
		t.Fatal(err)
	}
	write(store.MemoryWrite{
		Group:         &store.GroupMemory{ConversationKey: testConvKey, Summary: "泄露的摘要", CursorSeq: 9},
		SummaryGuards: []store.MemberVersion{{PlatformUserID: "20001", Version: 1}},
		Revisions:     summaryRev("新", "泄露的摘要"),
	})
	if g := group(); g.Summary != "新" || g.CursorSeq != 9 || len(summaryRevisions(t, db, testConvKey)) != 1 {
		t.Fatalf("guard broken: group %+v, want the stored summary kept, the cursor moved and no new revision", g)
	}

	fresh := store.ConversationKey(testBot, testPlatform, "private", "private_20003")
	if conflicts, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: fresh,
		Group:         &store.GroupMemory{ConversationKey: fresh, Summary: "泄露的摘要", CursorSeq: 3},
		SummaryGuards: []store.MemberVersion{{PlatformUserID: "20003", Version: 4}},
	}); err != nil || len(conflicts) != 0 {
		t.Fatalf("ApplyMemoryWrite = %v, %v", conflicts, err)
	}
	if g, err := db.GetGroupMemory(ctx, fresh); err != nil || g.Summary != "" || g.CursorSeq != 3 {
		t.Fatalf("broken guard without a stored row: %+v, %v; want an empty summary at cursor 3", g, err)
	}
}

func TestForgetPrivate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	priv := store.ConversationKey(testBot, testPlatform, "private", "private_20001")
	for _, key := range []string{priv, testConvKeyB} {
		if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: key,
			Group:   &store.GroupMemory{ConversationKey: key, Summary: "最近在聊钢琴", CursorSeq: 1, LastError: "parse_error"},
			Members: []store.MemberMemory{{ConversationKey: key, PlatformUserID: "20001", Note: "喜欢猫", Affinity: intPtrSQLite(40)}},
			Revisions: []store.MemoryRevision{
				{ConversationKey: key, Field: store.FieldSummary, NewValue: "最近在聊钢琴", Source: store.RevisionConsolidate},
				{ConversationKey: key, PlatformUserID: "20001", Field: store.FieldNote, NewValue: "喜欢猫", Source: store.RevisionConsolidate},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	msgs := []store.MessageRecord{memMessage(priv, "user_20001", "20001", false), memMessage(priv, "bot_30001", "30001", true),
		memMessage(testConvKeyB, "user_20001", "20001", false)}
	memInsertMessages(t, db, msgs...)
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	if err := db.ForgetPrivate(ctx, priv, "20001", at); err != nil {
		t.Fatalf("ForgetPrivate: %v", err)
	}
	g, err := db.GetGroupMemory(ctx, priv)
	if err != nil || g.Summary != "" || g.CursorSeq != latestSeq(t, db, priv) || g.LastError != "parse_error" {
		t.Fatalf("group after ForgetPrivate = %+v, %v; want the summary cleared and the cursor at seq %d", g, err, latestSeq(t, db, priv))
	}
	got, err := db.GetMemberMemories(ctx, priv, []string{"20001"})
	if m := got["20001"]; err != nil || m.Note != "" || m.Affinity != nil || !m.ForgottenAt.Equal(at) || m.Version != 2 {
		t.Fatalf("member after ForgetPrivate = %+v, %v", m, err)
	}
	for _, user := range []string{"", "20001"} {
		if revs, err := db.ListMemoryRevisions(ctx, priv, user); err != nil || len(revs) != 0 {
			t.Fatalf("revisions of %q = %+v, %v; want none", user, revs, err)
		}
	}
	if g, err := db.GetGroupMemory(ctx, testConvKeyB); err != nil || g.Summary != "最近在聊钢琴" || g.CursorSeq != 1 ||
		len(summaryRevisions(t, db, testConvKeyB)) != 1 {
		t.Fatalf("other conversation = %+v, %v; want untouched", g, err)
	}

	fresh := store.ConversationKey(testBot, testPlatform, "private", "private_20003")
	memInsertMessages(t, db, memMessage(fresh, "user_20003", "20003", false))
	if err := db.ForgetPrivate(ctx, fresh, "20003", at); err != nil {
		t.Fatalf("ForgetPrivate without a memory row: %v", err)
	}
	if g, err := db.GetGroupMemory(ctx, fresh); err != nil || g.Summary != "" || g.CursorSeq != latestSeq(t, db, fresh) {
		t.Fatalf("new group row = %+v, %v; want the cursor at the newest message", g, err)
	}
	empty := store.ConversationKey(testBot, testPlatform, "private", "private_29999")
	if err := db.ForgetPrivate(ctx, empty, "29999", at); err != nil {
		t.Fatalf("ForgetPrivate without messages: %v", err)
	}
	if g, err := db.GetGroupMemory(ctx, empty); err != nil || g.CursorSeq != 0 {
		t.Fatalf("group without messages = %+v, %v", g, err)
	}
}

func TestApplyMemoryWrite_CursorNeverMovesBack(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: testConvKey, Summary: "S", CursorSeq: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKey,
		Group: &store.GroupMemory{ConversationKey: testConvKey, Summary: "S2", CursorSeq: 4}}); err != nil {
		t.Fatal(err)
	}
	if g, err := db.GetGroupMemory(ctx, testConvKey); err != nil || g.CursorSeq != 9 || g.Summary != "S2" {
		t.Fatalf("group = %+v, %v; want cursor 9 kept", g, err)
	}
}

func intPtrSQLite(v int) *int { return &v }

func TestApplyMemoryWrite_KeepSummary(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: testConvKey, Summary: "已存的摘要", CursorSeq: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKey, KeepSummary: true,
		Group: &store.GroupMemory{ConversationKey: testConvKey, Summary: "过时的摘要", CursorSeq: 8, LastError: "model_error"},
	}); err != nil {
		t.Fatal(err)
	}
	if g, err := db.GetGroupMemory(ctx, testConvKey); err != nil || g.Summary != "已存的摘要" || g.CursorSeq != 8 || g.LastError != "model_error" {
		t.Fatalf("group = %+v, %v; want the stored summary with the new cursor and error", g, err)
	}
}
