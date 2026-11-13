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

func TestClearSummary(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, key := range []string{testConvKey, testConvKeyB} {
		if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: key,
			Group: &store.GroupMemory{ConversationKey: key, Summary: "最近在聊钢琴", CursorSeq: 7, LastError: "parse_error"},
			Revisions: []store.MemoryRevision{
				{ConversationKey: key, Field: store.FieldSummary, NewValue: "最近在聊钢琴", Source: store.RevisionConsolidate},
				{ConversationKey: key, PlatformUserID: "20001", Field: store.FieldNote, NewValue: "喜欢猫", Source: store.RevisionConsolidate},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ClearSummary(ctx, testConvKey); err != nil {
		t.Fatalf("ClearSummary: %v", err)
	}
	g, err := db.GetGroupMemory(ctx, testConvKey)
	if err != nil || g.Summary != "" || g.CursorSeq != 7 || g.LastError != "parse_error" {
		t.Fatalf("group after ClearSummary = %+v, %v; want only the summary cleared", g, err)
	}
	if revs := summaryRevisions(t, db, testConvKey); len(revs) != 0 {
		t.Fatalf("summary revisions = %+v, want none", revs)
	}
	if revs, err := db.ListMemoryRevisions(ctx, testConvKey, "20001"); err != nil || len(revs) != 1 {
		t.Fatalf("member revisions = %+v, %v; want kept", revs, err)
	}
	if g, err := db.GetGroupMemory(ctx, testConvKeyB); err != nil || g.Summary != "最近在聊钢琴" || len(summaryRevisions(t, db, testConvKeyB)) != 1 {
		t.Fatalf("other conversation = %+v, %v; want untouched", g, err)
	}

	none := store.ConversationKey(testBot, testPlatform, "private", "private_20003")
	if err := db.ClearSummary(ctx, none); err != nil {
		t.Fatalf("ClearSummary without a row: %v", err)
	}
	if _, err := db.GetGroupMemory(ctx, none); err == nil {
		t.Fatal("ClearSummary created a row")
	}
}

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
