package tools

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
)

// fakeVectors is a VectorSearcher returning hits or err, or waiting for its
// context to end when block is set, and recording its calls.
type fakeVectors struct {
	mu    sync.Mutex
	hits  []search.Hit
	err   error
	block bool
	calls []vectorCall
}

type vectorCall struct {
	convKey, query string
	r              search.Range
	limit          int
}

func (f *fakeVectors) Search(ctx context.Context, convKey, query string, r search.Range, limit int) ([]search.Hit, error) {
	f.mu.Lock()
	f.calls = append(f.calls, vectorCall{convKey, query, r, limit})
	hits, err, block := f.hits, f.err, f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return hits, err
}

func newRecallWith(st store.Store, vectors search.VectorSearcher) *recallTool {
	return NewRecall(RecallDeps{Store: st, Policies: newPolicies(st, true, true), Reviewer: review.New(nil, review.Options{}),
		Vectors: vectors}).(*recallTool)
}

// seq is the stored seq of m.
func seq(t testing.TB, st store.Store, m store.MessageRecord) int64 {
	t.Helper()
	got, err := st.GetMessage(context.Background(), m.BotInstanceID, m.ID)
	if err != nil {
		t.Fatalf("GetMessage %s: %v", m.ID, err)
	}
	return got.Seq
}

// local is the time in Asia/Shanghai.
func local(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, shanghai)
}

func TestFuse(t *testing.T) {
	kw := []store.MessageHit{{Seq: 10, Score: 9}, {Seq: 50, Score: 8}, {Seq: 30, Score: 7}, {Seq: 70, Score: 6}}
	vec := []search.Hit{{FromSeq: 48, ToSeq: 52, Score: 0.9}, {FromSeq: 90, ToSeq: 95, Score: 0.8}, {FromSeq: 94, ToSeq: 99, Score: 0.7}}
	got := fuse(kw, vec)
	type c struct {
		from, to int64
		anchors  []int64
	}
	var shape []c
	for _, x := range got {
		shape = append(shape, c{x.from, x.to, x.anchors})
	}
	want := []c{{48, 52, []int64{50}}, {90, 99, nil}, {10, 10, []int64{10}}, {30, 30, []int64{30}}, {70, 70, []int64{70}}}
	if fmt.Sprint(shape) != fmt.Sprint(want) {
		t.Fatalf("fused = %v, want %v", shape, want)
	}
	if !(got[0].score > got[1].score && got[1].score > got[2].score) {
		t.Fatalf("scores = %v", got)
	}

	tied := fuse([]store.MessageHit{{Seq: 5, Score: 1}, {Seq: 900, Score: 1}}, nil)
	if tied[0].from != 900 {
		t.Fatalf("near-equal hits = %v, want the newer first", tied)
	}
	clear := fuse([]store.MessageHit{{Seq: 5, Score: 9}, {Seq: 6, Score: 8}, {Seq: 7, Score: 7}, {Seq: 900, Score: 1}}, nil)
	if clear[0].from != 5 {
		t.Fatalf("ranked = %v, want relevance to outweigh recency", clear)
	}
}

func TestRecall_VectorOnlyHitShowsTheWholeStretch(t *testing.T) {
	st := newStore(t)
	msgs := []store.MessageRecord{
		message(groupKey, "20003", "小刚", "早上好", at(0)),
		message(groupKey, "20001", "小明", "周末打算出门逛逛", at(1)),
		message(groupKey, "20002", "小红", "去哪里呀", at(2)),
		message(groupKey, "20001", "小明", "公园吧", at(3)),
		message(groupKey, "20003", "小刚", "下雨了", at(30)),
	}
	addMessages(t, st, msgs...)
	vec := &fakeVectors{hits: []search.Hit{{FromSeq: seq(t, st, msgs[1]), ToSeq: seq(t, st, msgs[3]), Score: 0.82}}}
	got := call(t, newRecallWith(st, vec), invocation(groupKey, "20003", "小刚"), `{"query":"出去玩"}`)
	want := day1113 + "\n[11-13 09:01] 小明：周末打算出门逛逛\n[11-13 09:02] 小红：去哪里呀\n[11-13 09:03] 小明：公园吧"
	if got != want {
		t.Fatalf("recall =\n%s\nwant\n%s", got, want)
	}
	if len(vec.calls) != 1 || vec.calls[0] != (vectorCall{groupKey, "出去玩", search.Range{}, 20}) {
		t.Fatalf("vector calls = %+v", vec.calls)
	}
	if got := call(t, newRecall(st, true), invocation(groupKey, "20003", "小刚"), `{"query":"出去玩"}`); got != recallNotFound {
		t.Fatalf("keywords alone = %q", got)
	}
}

func TestRecall_FusesKeywordAndVectorHits(t *testing.T) {
	st := newStore(t)
	var msgs []store.MessageRecord
	for i := range 60 {
		text := fmt.Sprintf("闲聊第%d句", i)
		switch i {
		case 10, 20, 30, 40, 50:
			text = fmt.Sprintf("第%d句说到草莓", i)
		case 55:
			text = "周末去摘果子"
		}
		msgs = append(msgs, message(groupKey, "20001", "小明", text, at(i)))
	}
	addMessages(t, st, msgs...)
	vec := &fakeVectors{hits: []search.Hit{
		{FromSeq: seq(t, st, msgs[55]), ToSeq: seq(t, st, msgs[55]), Score: 0.9},
		{FromSeq: seq(t, st, msgs[9]), ToSeq: seq(t, st, msgs[11]), Score: 0.8},
	}}
	tool := newRecallWith(st, vec)
	tool.maxGroups = 2
	got := call(t, tool, invocation(groupKey, "20002", "小红"), `{"query":"草莓"}`)
	if !strings.Contains(got, "第10句说到草莓") || !strings.Contains(got, "周末去摘果子") || strings.Contains(got, "第50句") {
		t.Fatalf("recall kept the wrong two groups:\n%s", got)
	}
}

func TestRecall_VectorHitsGoThroughEveryFilter(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	msgs := []store.MessageRecord{
		message(groupKey, "20002", "小红", "我家住在幸福路", at(0)),
		message(groupKey, "20001", "小明", "/记忆", at(1)),
		message(groupKey, "", "", "我记得关于你的：喜欢爬山", at(1).Add(5*time.Second)),
		message(groupKey, "20003", "小刚", "周末一起爬山吗 电话 13800001111", at(2)),
		message(groupKey, "20001", "小明", "好呀", at(3)),
	}
	addMessages(t, st, msgs...)
	other := message(otherKey, "20001", "小明", "别的群的秘密", at(4))
	addMessages(t, st, other)
	if err := st.ForgetMember(ctx, groupKey, "20002", at(10)); err != nil {
		t.Fatal(err)
	}
	vec := &fakeVectors{hits: []search.Hit{
		{FromSeq: seq(t, st, msgs[0]), ToSeq: seq(t, st, msgs[4]), Score: 0.9},
		{FromSeq: seq(t, st, other), ToSeq: seq(t, st, other), Score: 0.8},
	}}
	got := call(t, newRecallWith(st, vec), invocation(groupKey, "20003", "小刚"), `{"query":"出去玩"}`)
	want := day1113 + "\n[11-13 09:02] 小刚：周末一起爬山吗 ［联系方式］\n[11-13 09:03] 小明：好呀"
	if got != want {
		t.Fatalf("recall =\n%s\nwant\n%s", got, want)
	}
	assertNoIDs(t, got)

	vec.hits = []search.Hit{{FromSeq: seq(t, st, other), ToSeq: seq(t, st, other), Score: 0.8}}
	if got := call(t, newRecallWith(st, vec), invocation(groupKey, "20003", "小刚"), `{"query":"秘密"}`); got != recallNotFound {
		t.Fatalf("recall showed another conversation's stretch:\n%s", got)
	}
}

func TestRecall_SkipsRepliesToCommandsOutsideTheLoadedMessages(t *testing.T) {
	st := newStore(t)
	cmd := at(0)
	msgs := []store.MessageRecord{message(groupKey, "20003", "小刚", "闲聊", cmd.Add(-time.Hour))}
	msgs = append(msgs, message(groupKey, "20001", "小明", "/记忆", cmd))
	for i := range 14 {
		msgs = append(msgs, message(groupKey, "20002", "小红", fmt.Sprintf("刷屏%d", i), cmd.Add(time.Duration(5+5*i)*time.Second)))
	}
	msgs = append(msgs, message(groupKey, "", "", "我记得关于你的：喜欢草莓，怕黑", cmd.Add(80*time.Second)))
	for i := range 14 {
		msgs = append(msgs, message(groupKey, "20002", "小红", fmt.Sprintf("后来%d", i), cmd.Add(time.Duration(10+i)*time.Minute)))
	}
	addMessages(t, st, msgs...)
	if got := call(t, newRecall(st, true), invocation(groupKey, "20003", "小刚"), `{"query":"怕黑"}`); got != recallNotFound {
		t.Fatalf("recall showed a reply to a command sent 15 messages earlier:\n%s", got)
	}
	vec := &fakeVectors{hits: []search.Hit{{FromSeq: seq(t, st, msgs[16]), ToSeq: seq(t, st, msgs[18]), Score: 0.9}}}
	got := call(t, newRecallWith(st, vec), invocation(groupKey, "20003", "小刚"), `{"query":"出去玩"}`)
	if strings.Contains(got, "怕黑") || !strings.Contains(got, "后来0") {
		t.Fatalf("recall of the stretch after the reply =\n%s", got)
	}
}

func TestRecall_FallsBackToKeywordsWhenVectorsFail(t *testing.T) {
	st := newStore(t)
	addMessages(t, st, message(groupKey, "20001", "小明", "我喜欢草莓", at(0)))
	want := day1113 + "\n[11-13 09:00] 小明：我喜欢草莓"
	inv := invocation(groupKey, "20002", "小红")
	if got := call(t, newRecallWith(st, &fakeVectors{err: errors.New("test: embedding server down")}), inv, `{"query":"草莓"}`); got != want {
		t.Fatalf("recall with failing vectors = %q", got)
	}
	defer func(d time.Duration) { recallVectorTimeout = d }(recallVectorTimeout)
	recallVectorTimeout = 50 * time.Millisecond
	start := time.Now()
	if got := call(t, newRecallWith(st, &fakeVectors{block: true}), inv, `{"query":"草莓"}`); got != want {
		t.Fatalf("recall with hanging vectors = %q", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("recall with hanging vectors took %v", d)
	}
}

func TestRecall_TimeRange(t *testing.T) {
	st := newStore(t)
	var msgs []store.MessageRecord
	add := func(text string, at time.Time) {
		msgs = append(msgs, message(groupKey, "20001", "小明", text, at))
		for i := range 3 {
			msgs = append(msgs, message(groupKey, "20002", "小红", "嗯", at.Add(time.Duration(i+1)*time.Second)))
		}
	}
	add("三月一号吃草莓", local(2026, 3, 1, 10, 0))
	add("三月二号深夜吃草莓", local(2026, 3, 2, 23, 59))
	add("三月三号零点吃草莓", local(2026, 3, 3, 0, 0))
	add("三月五号吃草莓", local(2026, 3, 5, 12, 0))
	addMessages(t, st, msgs...)
	vec := &fakeVectors{}
	tool := newRecallWith(st, vec)
	inv := invocation(groupKey, "20003", "小刚")
	for _, c := range []struct {
		args string
		want []string
	}{
		{`{"query":"草莓"}`, []string{"一号", "二号", "三号", "五号"}},
		{`{"query":"草莓","after":"2026-03-02","before":"2026-03-02"}`, []string{"二号"}},
		{`{"query":"草莓","before":"2026-03-02"}`, []string{"一号", "二号"}},
		{`{"query":"草莓","after":"2026-03-03"}`, []string{"三号", "五号"}},
		{`{"query":"草莓","after":"2026-03-04","before":"2026-03-04"}`, nil},
	} {
		got := call(t, tool, inv, c.args)
		for _, day := range []string{"一号", "二号", "三号", "五号"} {
			if want := slices.Contains(c.want, day); strings.Contains(got, day) != want {
				t.Errorf("recall(%s) shows %s = %v, want %v:\n%s", c.args, day, !want, want, got)
			}
		}
	}
	wantRange := search.Range{After: local(2026, 3, 2, 0, 0), Before: local(2026, 3, 3, 0, 0)}
	if r := vec.calls[1].r; !r.After.Equal(wantRange.After) || !r.Before.Equal(wantRange.Before) {
		t.Fatalf("vector range = %+v, want %+v", r, wantRange)
	}
}

func TestRecall_DateHeaders(t *testing.T) {
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20001", "小明", "草莓一", local(2026, 3, 1, 23, 58)),
		message(groupKey, "20002", "小红", "晚安", local(2026, 3, 2, 0, 1)),
		message(groupKey, "20002", "小红", "早", local(2026, 3, 2, 8, 0)),
		message(groupKey, "20002", "小红", "午饭", local(2026, 3, 2, 12, 0)),
		message(groupKey, "20002", "小红", "下午好", local(2026, 5, 20, 11, 0)),
		message(groupKey, "20002", "小红", "吃了吗", local(2026, 5, 20, 11, 30)),
		message(groupKey, "20002", "小红", "草莓二", local(2026, 5, 20, 12, 0)),
	)
	got := call(t, newRecall(st, true), invocation(groupKey, "20003", "小刚"), `{"query":"草莓"}`)
	want := strings.Join([]string{
		"—— 2026-03-01 ——",
		"[03-01 23:58] 小明：草莓一",
		"[03-02 00:01] 小红：晚安",
		"[03-02 08:00] 小红：早",
		"",
		"—— 2026-05-20 ——",
		"[05-20 11:00] 小红：下午好",
		"[05-20 11:30] 小红：吃了吗",
		"[05-20 12:00] 小红：草莓二",
	}, "\n")
	if got != want {
		t.Fatalf("recall =\n%s\nwant\n%s", got, want)
	}
}

func TestRecall_MergesGroupsThatTouch(t *testing.T) {
	st := newStore(t)
	var msgs []store.MessageRecord
	for i := range 12 {
		text := fmt.Sprintf("闲聊第%d句", i)
		if i == 3 || i == 8 {
			text = fmt.Sprintf("第%d句说到草莓", i)
		}
		msgs = append(msgs, message(groupKey, "20001", "小明", text, at(i)))
	}
	addMessages(t, st, msgs...)
	got := call(t, newRecall(st, true), invocation(groupKey, "20002", "小红"), `{"query":"草莓"}`)
	if strings.Count(got, "——") != 2 || strings.Count(got, "\n") != 10 || !strings.Contains(got, "闲聊第1句") || !strings.Contains(got, "闲聊第10句") {
		t.Fatalf("recall of two hits whose context touches =\n%s\nwant one group of 10 lines", got)
	}
}

func TestRecall_ObservesKeywordSearchTime(t *testing.T) {
	st := newStore(t)
	addMessages(t, st, message(groupKey, "20001", "小明", "我喜欢草莓", at(0)))
	reg := prometheus.NewRegistry()
	tool := NewRecall(RecallDeps{Store: st, Policies: newPolicies(st, true, true), Reviewer: review.New(nil, review.Options{}),
		Metrics: obs.NewMetrics(reg)})
	call(t, tool, invocation(groupKey, "20002", "小红"), `{"query":"草莓"}`)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "conloom_search_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if len(m.GetLabel()) == 1 && m.GetLabel()[0].GetValue() == "keyword" && m.GetHistogram().GetSampleCount() == 1 {
				return
			}
		}
	}
	t.Fatal("conloom_search_seconds{path=\"keyword\"} not observed once")
}

// openBulk opens a fresh database and a second handle on it for writing
// messages in bulk, as a database filled before the keyword index existed.
func openBulk(t testing.TB) (*sqlite.DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conloom.db")
	st, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close(); _ = st.Close() })
	return st, raw
}

// bulkInsert writes msgs straight into messages in one transaction, without
// indexing them.
func bulkInsert(t testing.TB, raw *sql.DB, msgs []store.MessageRecord) {
	t.Helper()
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO messages (bot_instance_id, id, conversation_key, platform, platform_message_id, actor_id,
		platform_user_id, display_name, text, reply_to, mentions, attachments, from_bot, occurred_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '[]', '[]', ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		ts := m.OccurredAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
		if _, err := stmt.Exec(m.BotInstanceID, m.ID, m.ConversationKey, m.Platform, m.PlatformMessageID, m.ActorID,
			m.PlatformUserID, m.DisplayName, m.Text, m.FromBot, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func backfillAll(t testing.TB, st *sqlite.DB) {
	t.Helper()
	for {
		_, remaining, err := st.BackfillMessageIndex(context.Background(), 5000)
		if err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			return
		}
	}
}

func TestRecall_FindsVeryOldMessages(t *testing.T) {
	st, raw := openBulk(t)
	msgs := []store.MessageRecord{message(groupKey, "20001", "小明", "我小时候养过一只叫团子的猫", local(2025, 1, 2, 20, 0))}
	start := local(2025, 2, 1, 0, 0)
	for i := range 6000 {
		msgs = append(msgs, message(groupKey, "20002", "小红", fmt.Sprintf("闲聊第%d句", i), start.Add(time.Duration(i)*time.Minute)))
	}
	bulkInsert(t, raw, msgs)
	backfillAll(t, st)
	got := call(t, newRecall(st, true), invocation(groupKey, "20003", "小刚"), `{"query":"团子"}`)
	if !strings.HasPrefix(got, "—— 2025-01-02 ——\n[01-02 20:00] 小明：我小时候养过一只叫团子的猫") {
		t.Fatalf("recall of a message 6000 messages back =\n%s", got)
	}
}

// BenchmarkRecall_100k recalls in a conversation of 100,000 messages.
func BenchmarkRecall_100k(b *testing.B) {
	st, raw := openBulk(b)
	words := strings.Fields("今天 周末 火锅 草莓 蛋糕 电影 游戏 加班 考试 旅行 出去玩 海边 爬山 下雨 猫猫 唱歌 跑步 代码 地铁 生日 快递 外卖 感冒")
	msgs := make([]store.MessageRecord, 100_000)
	start := local(2025, 1, 1, 0, 0)
	for i := range msgs {
		user := fmt.Sprint(20001 + i%3)
		text := words[i%len(words)] + "，" + words[(i*7)%len(words)] + "，" + words[(i*13)%len(words)]
		msgs[i] = message(groupKey, user, "群友"+user, text, start.Add(time.Duration(i)*time.Minute))
	}
	bulkInsert(b, raw, msgs)
	backfillAll(b, st)
	tool := newRecall(st, true)
	inv := invocation(groupKey, "20002", "小红")
	for b.Loop() {
		got, err := tool.Call(context.Background(), inv, []byte(`{"query":"周末出去玩","keywords":["旅行","海边"]}`))
		if err != nil || got == recallNotFound {
			b.Fatalf("recall = %q, %v", got, err)
		}
	}
}
