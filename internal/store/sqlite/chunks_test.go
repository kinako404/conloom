package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var testPrivateKey = store.ConversationKey(testBot, testPlatform, conloomapi.ConvPrivate, "conv_20001")

// chunkAt is 09:00 plus minutes on 2026-11-13 UTC.
func chunkAt(minutes int) time.Time { return time.Date(2026, 11, 13, 9, minutes, 0, 0, time.UTC) }

func testChunk(convKey string, from, to int64, start, end time.Time, model string) store.MemoryChunk {
	return store.MemoryChunk{ConversationKey: convKey, StartSeq: from, EndSeq: to, StartAt: start, EndAt: end,
		TextHash: "h", Model: model, Vector: []float32{1, -0.5, 0.25}}
}

func writeChunks(t *testing.T, db *DB, w store.ChunkWrite) {
	t.Helper()
	if w.ForgetStamp == "" {
		stamp, err := db.ForgetStamp(context.Background(), w.ConversationKey)
		if err != nil {
			t.Fatal(err)
		}
		w.ForgetStamp = stamp
	}
	ok, err := db.WriteChunks(context.Background(), w)
	if err != nil || !ok {
		t.Fatalf("WriteChunks = %v, %v", ok, err)
	}
}

func chunkRanges(t *testing.T, db *DB, convKey, model string) [][2]int64 {
	t.Helper()
	chunks, err := db.LoadChunks(context.Background(), convKey, model, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out [][2]int64
	for _, c := range chunks {
		out = append(out, [2]int64{c.StartSeq, c.EndSeq})
	}
	return out
}

func rebuildRanges(t *testing.T, db *DB, convKey string) [][2]int64 {
	t.Helper()
	rs, err := db.ChunkRebuilds(context.Background(), convKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out [][2]int64
	for _, r := range rs {
		out = append(out, [2]int64{r.StartSeq, r.EndSeq})
	}
	return out
}

// chunkMessages stores one message per entry of users ("" is the bot) in
// convKey, a minute apart from 09:00, and returns their seqs.
func chunkMessages(t *testing.T, db *DB, convKey string, users ...string) []int64 {
	t.Helper()
	var seqs []int64
	for i, u := range users {
		m := memMessage(convKey, "user_"+u, u, u == "")
		if u == "" {
			m.ActorID, m.PlatformUserID = "bot_30001", "30001"
		}
		m.OccurredAt = chunkAt(i)
		memInsertMessages(t, db, m)
		seqs = append(seqs, latestSeq(t, db, convKey))
	}
	return seqs
}

func TestChunks_WriteLoadAndCursor(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if n := countRows(t, db, "memory_chunks"); n != 0 {
		t.Fatalf("memory_chunks rows = %d", n)
	}
	st, err := db.ChunkState(ctx, testConvKey)
	if err != nil || st != (store.ChunkState{}) {
		t.Fatalf("ChunkState of a new conversation = %+v, %v", st, err)
	}

	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", CursorSeq: 9,
		Chunks: []store.MemoryChunk{testChunk(testConvKey, 1, 4, chunkAt(0), chunkAt(3), "m1"), testChunk(testConvKey, 5, 9, chunkAt(40), chunkAt(41), "m1")}})
	chunks, err := db.LoadChunks(ctx, testConvKey, "m1", 0)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("LoadChunks = %v, %v", chunks, err)
	}
	c := chunks[1]
	if c.ID <= chunks[0].ID || c.StartSeq != 5 || c.EndSeq != 9 || !c.StartAt.Equal(chunkAt(40)) || !c.EndAt.Equal(chunkAt(41)) ||
		c.Model != "m1" || c.TextHash != "h" || !slices.Equal(c.Vector, []float32{1, -0.5, 0.25}) || c.ConversationKey != testConvKey {
		t.Errorf("chunk = %+v", c)
	}
	if after, _ := db.LoadChunks(ctx, testConvKey, "m1", chunks[0].ID); len(after) != 1 || after[0].ID != c.ID {
		t.Errorf("LoadChunks after the first = %+v", after)
	}
	if other, _ := db.LoadChunks(ctx, testConvKey, "m2", 0); len(other) != 0 {
		t.Errorf("LoadChunks of another model = %+v", other)
	}
	if st, _ := db.ChunkState(ctx, testConvKey); st.CursorSeq != 9 {
		t.Errorf("cursor = %d, want 9", st.CursorSeq)
	}
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", CursorSeq: 5})
	if st, _ := db.ChunkState(ctx, testConvKey); st.CursorSeq != 9 {
		t.Errorf("cursor after moving back = %d, want it kept at 9", st.CursorSeq)
	}
}

func TestChunks_WriteSkippedAfterForgetting(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	stamp, err := db.ForgetStamp(ctx, testConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ForgetMember(ctx, testConvKey, "20001", chunkAt(5)); err != nil {
		t.Fatal(err)
	}
	newStamp, err := db.ForgetStamp(ctx, testConvKey)
	if err != nil || newStamp == stamp {
		t.Fatalf("ForgetStamp after forgetting = %q (was %q), %v", newStamp, stamp, err)
	}
	ok, err := db.WriteChunks(ctx, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", ForgetStamp: stamp, CursorSeq: 4,
		Chunks: []store.MemoryChunk{testChunk(testConvKey, 1, 4, chunkAt(0), chunkAt(3), "m1")}})
	if err != nil || ok {
		t.Fatalf("WriteChunks with a stale stamp = %v, %v; want skipped", ok, err)
	}
	if n := countRows(t, db, "memory_chunks"); n != 0 {
		t.Errorf("chunks = %d, want none", n)
	}
	if st, _ := db.ChunkState(ctx, testConvKey); st.CursorSeq != 0 {
		t.Errorf("cursor = %d, want unchanged", st.CursorSeq)
	}
	if again, _ := db.ForgetStamp(ctx, testConvKey); again != newStamp {
		t.Errorf("ForgetStamp is not stable: %q then %q", newStamp, again)
	}
	if other, _ := db.ForgetStamp(ctx, testConvKeyB); other != stamp {
		t.Errorf("another conversation's stamp changed: %q, want %q", other, stamp)
	}
}

func TestChunks_ForgetMemberDeletesAndQueuesChunksWithTheirMessages(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seqs := chunkMessages(t, db, testConvKey, "20001", "20002", "", "20002", "20001", "20002", "20002")
	other := chunkMessages(t, db, testConvKeyB, "20001", "20001")
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", Chunks: []store.MemoryChunk{
		testChunk(testConvKey, seqs[0], seqs[1], chunkAt(0), chunkAt(1), "m1"),
		testChunk(testConvKey, seqs[2], seqs[3], chunkAt(2), chunkAt(3), "m1"),
		testChunk(testConvKey, seqs[4], seqs[5], chunkAt(4), chunkAt(5), "m1"),
	}})
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m0", Chunks: []store.MemoryChunk{
		testChunk(testConvKey, seqs[6], seqs[6], chunkAt(6), chunkAt(6), "m0"),
	}})
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKeyB, Model: "m1", Chunks: []store.MemoryChunk{
		testChunk(testConvKeyB, other[0], other[1], chunkAt(0), chunkAt(1), "m1"),
	}})

	if err := db.ForgetMember(ctx, testConvKey, "20001", chunkAt(4)); err != nil {
		t.Fatal(err)
	}
	if got, want := chunkRanges(t, db, testConvKey, "m1"), [][2]int64{{seqs[2], seqs[3]}, {seqs[4], seqs[5]}}; !slices.Equal(got, want) {
		t.Errorf("chunks after forgetting = %v, want %v (only the one with 20001's earlier message deleted)", got, want)
	}
	if got, want := rebuildRanges(t, db, testConvKey), [][2]int64{{seqs[0], seqs[1]}}; !slices.Equal(got, want) {
		t.Errorf("rebuilds = %v, want %v", got, want)
	}
	if got := chunkRanges(t, db, testConvKeyB, "m1"); len(got) != 1 {
		t.Errorf("another conversation's chunks = %v, want kept", got)
	}

	if err := db.ForgetMember(ctx, testConvKey, "20002", chunkAt(30)); err != nil {
		t.Fatal(err)
	}
	if got := chunkRanges(t, db, testConvKey, "m1"); len(got) != 0 {
		t.Errorf("chunks after forgetting 20002 = %v, want none", got)
	}
	if got := chunkRanges(t, db, testConvKey, "m0"); len(got) != 0 {
		t.Errorf("chunks of an older model after forgetting 20002 = %v, want none", got)
	}
	want := [][2]int64{{seqs[6], seqs[6]}, {seqs[4], seqs[5]}, {seqs[2], seqs[3]}, {seqs[0], seqs[1]}}
	if got := rebuildRanges(t, db, testConvKey); !slices.Equal(got, want) {
		t.Errorf("rebuilds = %v, want %v, newest first", got, want)
	}
	n, err := db.CountChunkRebuilds(ctx)
	if err != nil || n != 4 {
		t.Errorf("CountChunkRebuilds = %d, %v", n, err)
	}
}

func TestChunks_ForgetPrivateDeletesEverythingBefore(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testPrivateKey, Model: "m1", Chunks: []store.MemoryChunk{
		testChunk(testPrivateKey, 1, 2, chunkAt(0), chunkAt(1), "m1"),
		testChunk(testPrivateKey, 3, 5, chunkAt(2), chunkAt(6), "m1"),
		testChunk(testPrivateKey, 6, 7, chunkAt(10), chunkAt(11), "m1"),
	}})
	if err := db.ForgetPrivate(ctx, testPrivateKey, "20001", chunkAt(4)); err != nil {
		t.Fatal(err)
	}
	if got, want := chunkRanges(t, db, testPrivateKey, "m1"), [][2]int64{{6, 7}}; !slices.Equal(got, want) {
		t.Errorf("chunks = %v, want %v", got, want)
	}
	if got, want := rebuildRanges(t, db, testPrivateKey), [][2]int64{{3, 5}}; !slices.Equal(got, want) {
		t.Errorf("rebuilds = %v, want %v (the chunk that runs past the forgetting)", got, want)
	}
}

func TestChunks_Rebuild(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", Chunks: []store.MemoryChunk{
		testChunk(testConvKey, 1, 4, chunkAt(0), chunkAt(3), "m1"),
		testChunk(testConvKey, 5, 9, chunkAt(5), chunkAt(9), "m1"),
	}})
	if err := db.ForgetPrivate(ctx, testConvKey, "20001", chunkAt(2)); err != nil {
		t.Fatal(err)
	}
	rs, err := db.ChunkRebuilds(ctx, testConvKey, 10)
	if err != nil || len(rs) != 1 || rs[0].StartSeq != 1 || rs[0].EndSeq != 4 {
		t.Fatalf("ChunkRebuilds = %+v, %v", rs, err)
	}
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", Rebuilt: rs,
		Chunks: []store.MemoryChunk{testChunk(testConvKey, 3, 4, chunkAt(2), chunkAt(3), "m1")}})
	if got, want := chunkRanges(t, db, testConvKey, "m1"), [][2]int64{{5, 9}, {3, 4}}; !slices.Equal(got, want) {
		t.Errorf("chunks = %v, want %v", got, want)
	}
	if got := rebuildRanges(t, db, testConvKey); len(got) != 0 {
		t.Errorf("rebuilds = %v, want none", got)
	}
}

func TestChunks_SwitchModel(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", CursorSeq: 9, Chunks: []store.MemoryChunk{
		testChunk(testConvKey, 1, 4, chunkAt(0), chunkAt(3), "m1"),
		testChunk(testConvKey, 5, 9, chunkAt(5), chunkAt(9), "m1"),
	}})
	if err := db.SwitchChunkModel(ctx, testConvKey, "m1"); err != nil {
		t.Fatal(err)
	}
	if st, _ := db.ChunkState(ctx, testConvKey); st != (store.ChunkState{CursorSeq: 9, Model: "m1"}) {
		t.Fatalf("state = %+v", st)
	}
	if got := rebuildRanges(t, db, testConvKey); len(got) != 0 {
		t.Fatalf("rebuilds for the same model = %v", got)
	}

	if err := db.SwitchChunkModel(ctx, testConvKey, "m2"); err != nil {
		t.Fatal(err)
	}
	if st, _ := db.ChunkState(ctx, testConvKey); st != (store.ChunkState{CursorSeq: 9, Model: "m2"}) {
		t.Fatalf("state = %+v", st)
	}
	if got, want := rebuildRanges(t, db, testConvKey), [][2]int64{{5, 9}, {1, 4}}; !slices.Equal(got, want) {
		t.Fatalf("rebuilds = %v, want %v", got, want)
	}
	if n, err := db.DeleteStaleChunks(ctx, testConvKey, "m2"); err != nil || n != 0 {
		t.Fatalf("DeleteStaleChunks with rebuilds pending = %d, %v; want nothing deleted", n, err)
	}
	rs, _ := db.ChunkRebuilds(ctx, testConvKey, 10)
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m2", Rebuilt: rs, Chunks: []store.MemoryChunk{
		testChunk(testConvKey, 1, 4, chunkAt(0), chunkAt(3), "m2"),
		testChunk(testConvKey, 5, 9, chunkAt(5), chunkAt(9), "m2"),
	}})
	if got := chunkRanges(t, db, testConvKey, "m1"); len(got) != 2 {
		t.Fatalf("old chunks before DeleteStaleChunks = %v", got)
	}
	if n, err := db.DeleteStaleChunks(ctx, testConvKey, "m2"); err != nil || n != 2 {
		t.Fatalf("DeleteStaleChunks = %d, %v; want 2", n, err)
	}
	if got := chunkRanges(t, db, testConvKey, "m1"); len(got) != 0 {
		t.Errorf("old chunks = %v, want none", got)
	}
	if got := chunkRanges(t, db, testConvKey, "m2"); len(got) != 2 {
		t.Errorf("new chunks = %v", got)
	}
}

func TestChunks_Stamp(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	s, err := db.ChunkStamp(ctx, testConvKey, "m1", 0)
	if err != nil || s != (store.ChunkStamp{}) {
		t.Fatalf("ChunkStamp of nothing = %+v, %v", s, err)
	}
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", Chunks: []store.MemoryChunk{
		testChunk(testConvKey, 1, 4, chunkAt(0), chunkAt(3), "m1"),
		testChunk(testConvKey, 5, 9, chunkAt(5), chunkAt(9), "m1"),
	}})
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m0", Chunks: []store.MemoryChunk{
		testChunk(testConvKey, 10, 11, chunkAt(10), chunkAt(11), "m0"),
	}})
	chunks, _ := db.LoadChunks(ctx, testConvKey, "m1", 0)
	s, err = db.ChunkStamp(ctx, testConvKey, "m1", chunks[0].ID)
	if err != nil || s != (store.ChunkStamp{Count: 2, MaxID: chunks[1].ID, UpTo: 1}) {
		t.Fatalf("ChunkStamp = %+v, %v", s, err)
	}
}

func TestMessagesUpTo(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seqs := chunkMessages(t, db, testConvKey, "20001", "20002", "20001", "20002")
	chunkMessages(t, db, testConvKeyB, "20001")
	got, err := db.MessagesUpTo(ctx, testConvKey, seqs[2], 2)
	if err != nil || len(got) != 2 || got[0].Seq != seqs[1] || got[1].Seq != seqs[2] {
		t.Fatalf("MessagesUpTo = %+v, %v", got, err)
	}
	if got, _ := db.MessagesUpTo(ctx, testConvKey, seqs[2], 0); got != nil {
		t.Errorf("limit 0 = %v", got)
	}
}

// oldForget forgets the user as a binary without semantic search would:
// member memory only, no chunks touched.
func oldForget(t *testing.T, db *DB, convKey, userID string, at time.Time) {
	t.Helper()
	if err := db.inTx(context.Background(), func(tx *sql.Tx) error {
		return forgetMember(context.Background(), tx, convKey, userID, at)
	}); err != nil {
		t.Fatal(err)
	}
}

func reconcile(t *testing.T, db *DB) int {
	t.Helper()
	n, err := db.ReconcileForgottenChunks(context.Background())
	if err != nil {
		t.Fatalf("ReconcileForgottenChunks: %v", err)
	}
	return n
}

func TestChunks_ReconcileForgetting(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	seqs := chunkMessages(t, db, testConvKey, "20001", "20002", "20001", "20001")
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", Chunks: []store.MemoryChunk{
		testChunk(testConvKey, seqs[0], seqs[2], chunkAt(0), chunkAt(2), "m1"),
		testChunk(testConvKey, seqs[3], seqs[3], chunkAt(3), chunkAt(3), "m1"),
	}})
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testPrivateKey, Model: "m1", Chunks: []store.MemoryChunk{
		testChunk(testPrivateKey, 100, 101, chunkAt(0), chunkAt(1), "m1"),
		testChunk(testPrivateKey, 102, 103, chunkAt(2), chunkAt(6), "m1"),
		testChunk(testPrivateKey, 104, 105, chunkAt(10), chunkAt(11), "m1"),
	}})
	if n := reconcile(t, db); n != 0 {
		t.Fatalf("reconciled %d members with nobody forgotten", n)
	}

	oldForget(t, db, testConvKey, "20002", chunkAt(30))
	oldForget(t, db, testPrivateKey, "20001", chunkAt(4))
	if n := reconcile(t, db); n != 2 {
		t.Fatalf("reconciled %d members, want 2", n)
	}
	if got, want := chunkRanges(t, db, testConvKey, "m1"), [][2]int64{{seqs[3], seqs[3]}}; !slices.Equal(got, want) {
		t.Errorf("group chunks = %v, want %v", got, want)
	}
	if got, want := rebuildRanges(t, db, testConvKey), [][2]int64{{seqs[0], seqs[2]}}; !slices.Equal(got, want) {
		t.Errorf("group rebuilds = %v, want %v", got, want)
	}
	if got, want := chunkRanges(t, db, testPrivateKey, "m1"), [][2]int64{{104, 105}}; !slices.Equal(got, want) {
		t.Errorf("private chunks = %v, want %v", got, want)
	}
	if got, want := rebuildRanges(t, db, testPrivateKey), [][2]int64{{102, 103}}; !slices.Equal(got, want) {
		t.Errorf("private rebuilds = %v, want %v", got, want)
	}

	rs, _ := db.ChunkRebuilds(ctx, testConvKey, 10)
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testConvKey, Model: "m1", Rebuilt: rs,
		Chunks: []store.MemoryChunk{testChunk(testConvKey, seqs[0], seqs[2], chunkAt(0), chunkAt(2), "m1")}})
	for range 2 {
		if n := reconcile(t, db); n != 0 {
			t.Fatalf("reconciling again handled %d members, want none", n)
		}
	}
	if got := chunkRanges(t, db, testConvKey, "m1"); len(got) != 2 {
		t.Errorf("rebuilt group chunks after reconciling again = %v, want both kept", got)
	}
	if got := rebuildRanges(t, db, testConvKey); len(got) != 0 {
		t.Errorf("group rebuilds after reconciling again = %v", got)
	}

	if err := db.ForgetMember(ctx, testConvKey, "20001", chunkAt(40)); err != nil {
		t.Fatal(err)
	}
	if n := reconcile(t, db); n != 0 {
		t.Errorf("reconciled %d members forgotten by this binary, want none", n)
	}
	oldForget(t, db, testConvKey, "20001", chunkAt(50))
	if n := reconcile(t, db); n != 1 {
		t.Errorf("reconciled %d members after forgetting again the old way, want 1", n)
	}
}
