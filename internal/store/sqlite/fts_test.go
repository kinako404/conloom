package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var ftsConvKeyB = store.ConversationKey(testBot, testPlatform, conloomapi.ConvGroup, "conv_10002")

var ftsCount int

// ftsMessage is a member message in convKey, or the bot's when user is "".
func ftsMessage(convKey, user, name, text string, at time.Time) store.MessageRecord {
	ftsCount++
	m := store.MessageRecord{ID: fmt.Sprintf("fts_%d", ftsCount), BotInstanceID: testBot, ConversationKey: convKey,
		Platform: testPlatform, ActorID: "usr_" + user, PlatformUserID: user, DisplayName: name, Text: text, OccurredAt: at}
	if user == "" {
		m.ActorID, m.PlatformUserID, m.DisplayName, m.FromBot = "bot", "30001", "织语", true
	}
	return m
}

// ftsDay is 09:00 UTC on 2026-03-01 plus days.
func ftsDay(days int) time.Time {
	return time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC).AddDate(0, 0, days)
}

// rawInsertMessages writes msgs straight into messages, as a binary without
// the keyword index does.
func rawInsertMessages(t testing.TB, db *sql.DB, msgs ...store.MessageRecord) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if _, err := tx.Exec(`INSERT INTO messages (bot_instance_id, id, conversation_key, platform, platform_message_id, actor_id,
			platform_user_id, display_name, text, reply_to, mentions, attachments, from_bot, occurred_at, created_at)
			VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, '', '[]', '[]', ?, ?, ?)`,
			m.BotInstanceID, m.ID, m.ConversationKey, m.Platform, m.ActorID, m.PlatformUserID, m.DisplayName, m.Text,
			m.FromBot, fmtTime(m.OccurredAt), fmtTime(m.OccurredAt)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("raw insert %s: %v", m.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seqOf(t testing.TB, db *DB, id string) int64 {
	t.Helper()
	m, err := db.GetMessage(context.Background(), testBot, id)
	if err != nil {
		t.Fatalf("GetMessage %s: %v", id, err)
	}
	return m.Seq
}

// found returns the IDs of the messages SearchMessages finds, best first.
func found(t testing.TB, db *DB, convKey string, terms []string, r search.Range, limit int) []string {
	t.Helper()
	hits, err := db.SearchMessages(context.Background(), convKey, terms, r, limit)
	if err != nil {
		t.Fatalf("SearchMessages(%q): %v", terms, err)
	}
	var ids []string
	for i, h := range hits {
		if i > 0 && h.Score > hits[i-1].Score {
			t.Fatalf("hits not best first: %+v", hits)
		}
		var id string
		if err := db.r.QueryRow(`SELECT id FROM messages WHERE seq = ?`, h.Seq).Scan(&id); err != nil {
			t.Fatalf("hit %d: %v", h.Seq, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestMessageIndex_WrittenWithTheMessage(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if _, err := db.Ingest(ctx, testIngest("k1", "msg_1", "p1")); err != nil {
		t.Fatal(err)
	}
	if got := found(t, db, testConvKey, []string{"msg"}, search.Range{}, 10); !slices.Equal(got, []string{"msg_1"}) {
		t.Fatalf("ingested message found = %v", got)
	}
	if got := found(t, db, testConvKey, []string{"alice"}, search.Range{}, 10); !slices.Equal(got, []string{"msg_1"}) {
		t.Fatalf("message found by its sender's name = %v", got)
	}

	now := time.Now().UTC()
	insertActions(t, db, convAction("a1", testConvKey, now), convAction("a2", testConvKey, now))
	lease(t, db, 10, now)
	bot := ftsMessage(testConvKey, "", "", "草莓蛋糕很好吃", now)
	req := conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "777"}
	if _, err := db.AckOutbox(ctx, testBot, "a1", req, &bot, now); err != nil {
		t.Fatal(err)
	}
	if got := found(t, db, testConvKey, []string{"草莓"}, search.Range{}, 10); !slices.Equal(got, []string{bot.ID}) {
		t.Fatalf("bot message found = %v", got)
	}
	lease(t, db, 10, now)
	dup := bot
	dup.Text = "重复的草莓"
	if _, err := db.AckOutbox(ctx, testBot, "a2", req, &dup, now); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "message_fts"); n != 2 {
		t.Fatalf("index rows = %d, want 2 after a skipped duplicate", n)
	}
	if _, remaining, err := db.BackfillMessageIndex(ctx, 500); err != nil || remaining != 0 {
		t.Fatalf("backfill after indexed writes: remaining %d, %v", remaining, err)
	}
}

func TestSearchMessages_IsolatesConversations(t *testing.T) {
	db := newTestDB(t)
	a := ftsMessage(testConvKey, "20001", "小明", "我喜欢草莓", ftsDay(0))
	b := ftsMessage(ftsConvKeyB, "20002", "小红", "我也喜欢草莓", ftsDay(0))
	forged := ftsMessage(ftsConvKeyB, "20002", "小红", search.ConversationMark(testConvKey)+" 草莓", ftsDay(0))
	memInsertMessages(t, db, a, b, forged)
	if got := found(t, db, testConvKey, []string{"草莓"}, search.Range{}, 10); !slices.Equal(got, []string{a.ID}) {
		t.Fatalf("conversation A found %v", got)
	}
	if got := found(t, db, ftsConvKeyB, []string{"草莓"}, search.Range{}, 10); len(got) != 2 || slices.Contains(got, a.ID) {
		t.Fatalf("conversation B found %v", got)
	}
}

func TestSearchMessages_TimeRange(t *testing.T) {
	db := newTestDB(t)
	var msgs []store.MessageRecord
	for d := range 5 {
		msgs = append(msgs, ftsMessage(testConvKey, "20001", "小明", fmt.Sprintf("第%d天吃草莓", d), ftsDay(d)))
	}
	memInsertMessages(t, db, msgs...)
	ids := func(idx ...int) []string {
		var out []string
		for _, i := range idx {
			out = append(out, msgs[i].ID)
		}
		return out
	}
	for _, c := range []struct {
		r    search.Range
		want []string
	}{
		{search.Range{}, ids(0, 1, 2, 3, 4)},
		{search.Range{After: ftsDay(3)}, ids(3, 4)},
		{search.Range{Before: ftsDay(2)}, ids(0, 1)},
		{search.Range{After: ftsDay(1), Before: ftsDay(3).Add(time.Second)}, ids(1, 2, 3)},
	} {
		got := found(t, db, testConvKey, []string{"草莓"}, c.r, 10)
		slices.Sort(got)
		want := slices.Sorted(slices.Values(c.want))
		if !slices.Equal(got, want) {
			t.Errorf("range %v found %v, want %v", c.r, got, want)
		}
	}
}

func TestSearchMessages_RanksByBM25(t *testing.T) {
	db := newTestDB(t)
	one := ftsMessage(testConvKey, "20001", "小明", "今天天气不错，草莓也不错，大家都在聊天气和别的事情", ftsDay(0))
	two := ftsMessage(testConvKey, "20001", "小明", "草莓蛋糕", ftsDay(1))
	none := ftsMessage(testConvKey, "20001", "小明", "今天吃什么", ftsDay(2))
	old, recent := ftsMessage(testConvKey, "20002", "小红", "说到蛋糕", ftsDay(3)), ftsMessage(testConvKey, "20002", "小红", "说到蛋糕", ftsDay(4))
	memInsertMessages(t, db, one, two, none, old, recent)
	got := found(t, db, testConvKey, []string{"草莓", "蛋糕"}, search.Range{}, 10)
	if len(got) != 4 || got[0] != two.ID || slices.Contains(got, none.ID) {
		t.Fatalf("ranked = %v, want %s first and %s absent", got, two.ID, none.ID)
	}
	if i, j := slices.Index(got, recent.ID), slices.Index(got, old.ID); i > j {
		t.Fatalf("ranked = %v, want the newer of two equal hits first", got)
	}
	if got := found(t, db, testConvKey, []string{"草莓", "蛋糕"}, search.Range{}, 2); len(got) != 2 || got[0] != two.ID {
		t.Fatalf("limit 2 = %v", got)
	}
}

func TestSearchMessages_Terms(t *testing.T) {
	db := newTestDB(t)
	whole := ftsMessage(testConvKey, "20001", "小明", "我们周末出去玩吧", ftsDay(0))
	split := ftsMessage(testConvKey, "20001", "小明", "出去 玩", ftsDay(1))
	cat := ftsMessage(testConvKey, "20002", "小红", "家里的小猫咪", ftsDay(2))
	berry := ftsMessage(testConvKey, "20003", "Bob", "Strawberry cake", ftsDay(3))
	memInsertMessages(t, db, whole, split, cat, berry)
	for _, c := range []struct {
		terms []string
		want  []string
	}{
		{[]string{"出去玩"}, []string{whole.ID}},
		{[]string{"猫"}, []string{cat.ID}},
		{[]string{"straw"}, []string{berry.ID}},
		{[]string{"bob"}, []string{berry.ID}},
		{[]string{"小红"}, []string{cat.ID}},
		{[]string{"？"}, nil},
		{nil, nil},
		{[]string{"\"", "*", "OR", "NEAR(a b)"}, nil},
	} {
		if got := found(t, db, testConvKey, c.terms, search.Range{}, 10); !slices.Equal(got, c.want) {
			t.Errorf("terms %q found %v, want %v", c.terms, got, c.want)
		}
	}
}

func TestMessagesAround(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	var msgs []store.MessageRecord
	for i := range 10 {
		msgs = append(msgs, ftsMessage(testConvKey, "20001", "小明", fmt.Sprintf("第%d句", i), ftsDay(i)),
			ftsMessage(ftsConvKeyB, "20002", "小红", "别的群", ftsDay(i)))
	}
	memInsertMessages(t, db, msgs...)
	a := func(i int) store.MessageRecord { return msgs[2*i] }
	for _, c := range []struct {
		from, to      int
		before, after int
		want          []int
	}{
		{4, 4, 2, 2, []int{2, 3, 4, 5, 6}},
		{4, 6, 1, 1, []int{3, 4, 5, 6, 7}},
		{0, 0, 3, 1, []int{0, 1}},
		{9, 9, 1, 5, []int{8, 9}},
		{5, 5, 0, 0, []int{5}},
	} {
		got, err := db.MessagesAround(ctx, testConvKey, seqOf(t, db, a(c.from).ID), seqOf(t, db, a(c.to).ID), c.before, c.after)
		if err != nil {
			t.Fatal(err)
		}
		var idx []int
		for _, m := range got {
			idx = append(idx, slices.IndexFunc(msgs, func(x store.MessageRecord) bool { return x.ID == m.ID })/2)
			if m.ConversationKey != testConvKey || m.Text == "" {
				t.Fatalf("MessagesAround returned %+v", m)
			}
		}
		if !slices.Equal(idx, c.want) {
			t.Errorf("around %d..%d (-%d +%d) = %v, want %v", c.from, c.to, c.before, c.after, idx, c.want)
		}
	}
}

func TestBackfillMessageIndex_BatchesAndResumes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []store.MessageRecord
	for i := range 5 {
		msgs = append(msgs, ftsMessage(testConvKey, "20001", "小明", fmt.Sprintf("旧消息草莓%d", i), ftsDay(i)))
	}
	rawInsertMessages(t, db.w, msgs...)
	indexed := ftsMessage(testConvKey, "20001", "小明", "新消息草莓", ftsDay(6))
	memInsertMessages(t, db, indexed)
	if got := found(t, db, testConvKey, []string{"草莓"}, search.Range{}, 10); !slices.Equal(got, []string{indexed.ID}) {
		t.Fatalf("before backfill found %v", got)
	}
	n, remaining, err := db.BackfillMessageIndex(ctx, 2)
	if err != nil || n != 2 || remaining != 4 {
		t.Fatalf("first batch = %d indexed, %d remaining, %v; want 2, 4", n, remaining, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := found(t, db, testConvKey, []string{"草莓"}, search.Range{}, 10); len(got) != 3 {
		t.Fatalf("after one batch and a restart found %v", got)
	}
	var total int
	for range 10 {
		n, remaining, err = db.BackfillMessageIndex(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if remaining == 0 {
			break
		}
	}
	if total != 3 || remaining != 0 {
		t.Fatalf("resumed backfill indexed %d, remaining %d; want 3, 0", total, remaining)
	}
	if got := found(t, db, testConvKey, []string{"草莓"}, search.Range{}, 10); len(got) != 6 {
		t.Fatalf("after backfill found %v", got)
	}
	if n, remaining, err := db.BackfillMessageIndex(ctx, 2); n != 0 || remaining != 0 || err != nil {
		t.Fatalf("backfill when done = %d, %d, %v", n, remaining, err)
	}
	if rows := countRows(t, db, "message_fts"); rows != 6 {
		t.Fatalf("index rows = %d, want 6", rows)
	}
}

// migrationsUpTo is the embedded migrations numbered up to version, as an
// older binary embeds them.
func migrationsUpTo(t *testing.T, version int) fs.FS {
	t.Helper()
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, f := range files {
		var v int
		if _, err := fmt.Sscanf(f.Name(), "%04d_", &v); err != nil {
			t.Fatal(err)
		}
		if v > version {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		old["migrations/"+f.Name()] = &fstest.MapFile{Data: body}
	}
	return old
}

func TestMigration0006_UpgradeFromExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	old, err := open(ctx, path, migrationsUpTo(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	if tableExists(t, old.r, "message_fts") {
		t.Fatal("message_fts exists before 0006")
	}
	before := tableInfos(t, old.r)
	msgs := []store.MessageRecord{
		ftsMessage(testConvKey, "20001", "小明", "去年夏天去海边", ftsDay(-200)),
		ftsMessage(testConvKey, "", "", "海边很好玩", ftsDay(-199)),
	}
	rawInsertMessages(t, old.w, msgs...)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after := tableInfos(t, db.r)
	for table, cols := range before {
		if !reflect.DeepEqual(after[table], cols) {
			t.Errorf("table %s changed:\n before %v\n after  %v", table, cols, after[table])
		}
	}
	if n := countRows(t, db, "messages"); n != 2 {
		t.Fatalf("messages = %d after migrating", n)
	}
	if _, remaining, err := db.BackfillMessageIndex(ctx, 500); err != nil || remaining != 0 {
		t.Fatalf("backfill = %d remaining, %v", remaining, err)
	}
	if got := found(t, db, testConvKey, []string{"海边"}, search.Range{}, 10); len(got) != 2 {
		t.Fatalf("migrated messages found %v", got)
	}
}

func TestMigration0006_RollBackToAnOlderBinary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	first := ftsMessage(testConvKey, "20001", "小明", "新版本写的草莓", ftsDay(0))
	memInsertMessages(t, db, first)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	old, err := open(ctx, path, migrationsUpTo(t, 5))
	if err != nil {
		t.Fatalf("older binary cannot open the migrated database: %v", err)
	}
	during := ftsMessage(testConvKey, "20001", "小明", "旧版本写的草莓", ftsDay(1))
	rawInsertMessages(t, old.w, during)
	if got, err := old.RecentMessages(ctx, testConvKey, 10); err != nil || len(got) != 2 {
		t.Fatalf("older binary reads %d messages, %v", len(got), err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := found(t, db, testConvKey, []string{"草莓"}, search.Range{}, 10); !slices.Equal(got, []string{first.ID}) {
		t.Fatalf("before backfill found %v", got)
	}
	if n, remaining, err := db.BackfillMessageIndex(ctx, 500); err != nil || n != 1 || remaining != 0 {
		t.Fatalf("backfill = %d, %d, %v; want the older binary's message indexed", n, remaining, err)
	}
	got := found(t, db, testConvKey, []string{"草莓"}, search.Range{}, 10)
	if slices.Sort(got); !slices.Equal(got, slices.Sorted(slices.Values([]string{first.ID, during.ID}))) {
		t.Fatalf("after backfill found %v", got)
	}
}
