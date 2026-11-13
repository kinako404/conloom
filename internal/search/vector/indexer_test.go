package vector

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestIndexer_EmbedsClosedChunksInBatches(t *testing.T) {
	e := newEnv(t)
	var seqs []int64
	for i := range 20 {
		user, name := "20001", "小明"
		if i%2 == 1 {
			user, name = "20002", "小红"
		}
		seqs = append(seqs, e.say(groupKey, user, name, fmt.Sprintf("第%d句", i), at(i)))
	}
	e.now = at(19).Add(10 * time.Minute)
	e.run(e.indexer("m1", 1))

	inputs := e.emb.take()
	if len(inputs) != 2 || len(inputs[0]) != 1 || len(inputs[1]) != 1 {
		t.Fatalf("requests = %v, want two of one chunk (batch size 1; the third chunk is still open)", inputs)
	}
	want := "[11-13 09:00] 小明：第0句\n[11-13 09:01] 小红：第1句\n[11-13 09:02] 小明：第2句\n[11-13 09:03] 小红：第3句\n" +
		"[11-13 09:04] 小明：第4句\n[11-13 09:05] 小红：第5句\n[11-13 09:06] 小明：第6句\n[11-13 09:07] 小红：第7句"
	if inputs[0][0] != want {
		t.Errorf("first chunk =\n%s\nwant\n%s", inputs[0][0], want)
	}
	assertNoIDs(t, flat(inputs))
	if c := e.chunks(groupKey, "m1"); len(c) != 2 || c[0].StartSeq != seqs[0] || c[0].EndSeq != seqs[7] || c[1].EndSeq != seqs[15] ||
		!c[1].StartAt.Equal(at(8)) || !c[1].EndAt.Equal(at(15)) || len(c[0].Vector) != len(vocab)+1 {
		t.Fatalf("chunks = %+v", c)
	}
	if got := e.cursor(groupKey); got != seqs[15] {
		t.Errorf("cursor = %d, want %d (before the open chunk)", got, seqs[15])
	}
	if e.requests("ok") != 2 {
		t.Errorf("ok requests = %v, want 2", e.requests("ok"))
	}

	e.run(e.indexer("m1", 1))
	if in := e.emb.take(); len(in) != 0 {
		t.Fatalf("second round while the last chunk is open embedded %v", in)
	}
	e.now = at(19).Add(31 * time.Minute)
	e.run(e.indexer("m1", 16))
	in := e.emb.take()
	if len(in) != 1 || len(in[0]) != 1 || !strings.HasPrefix(in[0][0], "[11-13 09:16] 小明：第16句") {
		t.Fatalf("round after the pause embedded %v, want the last chunk", in)
	}
	if got := e.cursor(groupKey); got != seqs[19] {
		t.Errorf("cursor = %d, want %d", got, seqs[19])
	}

	for i := range 3 {
		e.say(groupKey, "20001", "小明", fmt.Sprintf("后来第%d句", i), at(200+i*40))
	}
	e.now = at(400)
	e.run(e.indexer("m1", 16))
	if in := e.emb.take(); len(in) != 1 || len(in[0]) != 3 {
		t.Fatalf("three chunks of one message each = %v, want one batch of 3", in)
	}
}

func TestIndexer_FiltersLikeRecall(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.setPolicy(groupKey, func(p *conloomapi.GroupPolicy) {
		p.Review.BlockedWords, p.Review.Action = []string{"坏词"}, review.VerdictBlock
	})
	e.say(groupKey, "20001", "小明", "/记忆", at(0))
	e.say(groupKey, "", "", "我记得关于你的：喜欢草莓蛋糕", at(0).Add(10*time.Second))
	e.say(groupKey, "20002", "小红", "我电话 139 0000 2222", at(1))
	e.say(groupKey, "20002", "小红", "这句有坏词", at(2))
	e.say(groupKey, "20001", "小明", "我想去旅行", at(3))
	e.say(groupKey, "20002", "20002", "我也想", at(4))
	if err := e.st.ForgetMember(ctx, groupKey, "20001", at(3).Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	e.say(groupKey, "20001", "小明", "火锅好吃", at(5))
	e.run(e.indexer("m1", 16))

	got := flat(e.emb.take())
	want := []string{"[11-13 09:01] 小红：我［联系方式］\n[11-13 09:04] 一位群友：我也想\n[11-13 09:05] 小明：火锅好吃"}
	if !slices.Equal(got, want) {
		t.Fatalf("embedded %q, want %q", got, want)
	}
	assertNoIDs(t, got)
}

func TestIndexer_OnlyConversationsWithMemoryInEffect(t *testing.T) {
	e := newEnv(t)
	e.say(groupKey, "20001", "小明", "草莓", at(0))
	e.say(group2Key, "20001", "小明", "猫", at(0))
	e.say(privateKey, "20001", "小明", "旅行", at(0))
	e.setPolicy(group2Key, func(p *conloomapi.GroupPolicy) { p.Memory.Enabled = false })
	e.override[privateKey] = func(p *conloomapi.GroupPolicy) { p.Concurrency.AllowLongRequests = false }
	e.run(e.indexer("m1", 16))

	if got := flat(e.emb.take()); !slices.Equal(got, []string{"[11-13 09:00] 小明：草莓"}) {
		t.Fatalf("embedded %q, want only the conversation with memory in effect", got)
	}
	if n := len(e.chunks(group2Key, "m1")) + len(e.chunks(privateKey, "m1")); n != 0 {
		t.Errorf("chunks without memory = %d", n)
	}
	if e.cursor(group2Key) != 0 || e.cursor(privateKey) != 0 {
		t.Error("cursor moved in a conversation without memory")
	}

	delete(e.override, privateKey)
	e.run(e.indexer("m1", 16))
	if got := flat(e.emb.take()); !slices.Equal(got, []string{"[11-13 09:00] 小明：旅行"}) {
		t.Fatalf("after memory took effect embedded %q", got)
	}
}

func TestIndexer_BacksOffAfterFailures(t *testing.T) {
	e := newEnv(t)
	e.say(groupKey, "20001", "小明", "草莓", at(0))
	ix := e.indexer("m1", 16)
	e.emb.set(func(f *fakeEmbedder) { f.fail = true })
	ctx := context.Background()

	start := e.now
	tick := func(after time.Duration) {
		e.now = start.Add(after)
		ix.tick(ctx)
	}
	var attempts []time.Duration
	for m := time.Duration(0); m <= 40*time.Minute; m += time.Minute {
		before := e.emb.requestCount()
		tick(m)
		if e.emb.requestCount() > before {
			attempts = append(attempts, m)
		}
	}
	want := []time.Duration{0, time.Minute, 3 * time.Minute, 7 * time.Minute, 15 * time.Minute, 25 * time.Minute, 35 * time.Minute}
	if !slices.Equal(attempts, want) {
		t.Fatalf("attempts at %v, want %v (doubling from 1m, at most 10m)", attempts, want)
	}
	if e.requests("error") != float64(len(want)) {
		t.Errorf("error requests = %v", e.requests("error"))
	}

	e.emb.set(func(f *fakeEmbedder) { f.fail = false })
	tick(45 * time.Minute)
	tick(46 * time.Minute)
	if len(e.chunks(groupKey, "m1")) != 1 {
		t.Fatal("no chunk after the service came back")
	}
	e.emb.set(func(f *fakeEmbedder) { f.fail = true })
	e.say(groupKey, "20001", "小明", "猫", at(100))
	before := e.emb.requestCount()
	tick(47 * time.Minute)
	tick(48 * time.Minute)
	if got := e.emb.requestCount() - before; got != 2 {
		t.Errorf("after a success the pause starts again at one minute: %d attempts, want 2", got)
	}
}

func TestIndexer_RoundBudget(t *testing.T) {
	e := newEnv(t)
	for i := range roundBudget + 10 {
		e.say(groupKey, "20001", "小明", fmt.Sprintf("第%d句", i), at(i*31))
	}
	e.now = at((roundBudget + 20) * 31)
	e.run(e.indexer("m1", 16))
	if got := len(flat(e.emb.take())); got != roundBudget {
		t.Fatalf("first round embedded %d chunks, want %d", got, roundBudget)
	}
	if got := e.metric("conloom_embedding_backlog", ""); got != 2 {
		t.Errorf("backlog = %v, want 2 (10 messages left, 8 to a chunk)", got)
	}
	e.run(e.indexer("m1", 16))
	if got := len(flat(e.emb.take())); got != 10 {
		t.Fatalf("second round embedded %d chunks, want 10", got)
	}
	if got := len(e.chunks(groupKey, "m1")); got != roundBudget+10 {
		t.Errorf("chunks = %d", got)
	}
	if got := e.metric("conloom_embedding_backlog", ""); got != 0 {
		t.Errorf("backlog = %v, want 0", got)
	}
}

func TestIndexer_ModelSwitch(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for i := range 3 {
		e.say(groupKey, "20001", "小明", "我喜欢草莓", at(i*40))
	}
	e.run(e.indexer("m1", 16))
	e.emb.take()
	if hits, _ := e.searcher("m1").Search(ctx, groupKey, "草莓", search.Range{}, 10); len(hits) != 3 {
		t.Fatalf("m1 hits = %v", hits)
	}

	if hits, err := e.searcher("m2").Search(ctx, groupKey, "草莓", search.Range{}, 10); err != nil || len(hits) != 0 {
		t.Fatalf("m2 hits before rebuilding = %v, %v; want none (old vectors are not used)", hits, err)
	}
	e.say(groupKey, "20001", "小明", "猫也可爱", at(500))
	e.emb.take()
	e.run(e.indexer("m2", 2))
	got := flat(e.emb.take())
	var prefixes []string
	for _, text := range got {
		prefix, _, _ := strings.Cut(text, " 小明")
		prefixes = append(prefixes, prefix)
	}
	if want := []string{"[11-13 10:20]", "[11-13 09:40]", "[11-13 09:00]", "[11-13 17:20]"}; !slices.Equal(prefixes, want) {
		t.Fatalf("m2 round embedded %q, want the three old chunks newest first, then the new one", got)
	}
	if n := len(e.chunks(groupKey, "m1")); n != 0 {
		t.Errorf("m1 chunks after rebuilding = %d, want deleted", n)
	}
	if n := len(e.chunks(groupKey, "m2")); n != 4 {
		t.Errorf("m2 chunks = %d, want 4", n)
	}
	hits, err := e.searcher("m2").Search(ctx, groupKey, "草莓", search.Range{}, 10)
	if err != nil || len(hits) != 4 || hits[0].Score <= hits[3].Score {
		t.Fatalf("m2 hits = %v, %v", hits, err)
	}
}

func TestIndexer_ModelSwitchKeepsOldVectorsUntilDone(t *testing.T) {
	e := newEnv(t)
	for i := range roundBudget + 5 {
		e.say(groupKey, "20001", "小明", "草莓", at(i*31))
	}
	e.now = at((roundBudget + 20) * 31)
	e.run(e.indexer("m1", 64))
	e.run(e.indexer("m1", 64))
	e.emb.take()
	if n := len(e.chunks(groupKey, "m1")); n != roundBudget+5 {
		t.Fatalf("m1 chunks = %d", n)
	}
	e.run(e.indexer("m2", 64))
	if n := len(e.chunks(groupKey, "m1")); n != roundBudget+5 {
		t.Errorf("m1 chunks while rebuilding = %d, want all kept", n)
	}
	if n := len(e.chunks(groupKey, "m2")); n != roundBudget {
		t.Errorf("m2 chunks after one round = %d, want %d", n, roundBudget)
	}
	e.run(e.indexer("m2", 64))
	if n := len(e.chunks(groupKey, "m1")); n != 0 {
		t.Errorf("m1 chunks after rebuilding = %d, want none", n)
	}
	if n := len(e.chunks(groupKey, "m2")); n != roundBudget+5 {
		t.Errorf("m2 chunks = %d", n)
	}
}

func TestIndexer_GroupForgettingRebuildsWithoutTheMember(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.say(groupKey, "20001", "小明", "我喜欢草莓", at(0))
	e.say(groupKey, "20002", "小红", "我养了一只猫", at(1))
	e.say(groupKey, "", "", "猫真可爱", at(2))
	e.say(groupKey, "20001", "小明", "我想去旅行", at(60))
	e.run(e.indexer("m1", 16))
	e.emb.take()
	s := e.searcher("m1")
	if hits, _ := s.Search(ctx, groupKey, "猫", search.Range{}, 1); len(hits) != 1 {
		t.Fatalf("hits before forgetting = %v", hits)
	}

	if err := e.st.ForgetMember(ctx, groupKey, "20002", at(30)); err != nil {
		t.Fatal(err)
	}
	if c := e.chunks(groupKey, "m1"); len(c) != 1 || !c[0].StartAt.Equal(at(60)) {
		t.Fatalf("chunks after forgetting = %+v, want only the one without 小红", c)
	}
	hits, _ := s.Search(ctx, groupKey, "猫", search.Range{}, 5)
	if len(hits) != 1 || hits[0].FromSeq != e.chunks(groupKey, "m1")[0].StartSeq {
		t.Fatalf("hits right after forgetting = %v, want only the kept chunk", hits)
	}

	e.emb.take()
	e.run(e.indexer("m1", 16))
	got := flat(e.emb.take())
	if want := []string{"[11-13 09:00] 小明：我喜欢草莓\n[11-13 09:02] 你：猫真可爱"}; !slices.Equal(got, want) {
		t.Fatalf("rebuilt %q, want %q", got, want)
	}
	if n := len(e.chunks(groupKey, "m1")); n != 2 {
		t.Errorf("chunks after rebuilding = %d, want 2", n)
	}
	if hits, _ := s.Search(ctx, groupKey, "猫", search.Range{}, 5); len(hits) != 2 {
		t.Errorf("hits after rebuilding = %v", hits)
	}
}

func TestIndexer_PrivateForgettingDeletesEverythingBefore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.say(privateKey, "20001", "小明", "我喜欢草莓", at(0))
	e.say(privateKey, "", "", "草莓很甜", at(1))
	e.say(privateKey, "20001", "小明", "我想去旅行", at(60))
	e.run(e.indexer("m1", 16))
	e.emb.take()
	if n := len(e.chunks(privateKey, "m1")); n != 2 {
		t.Fatalf("chunks = %d", n)
	}
	if err := e.st.ForgetPrivate(ctx, privateKey, "20001", at(61)); err != nil {
		t.Fatal(err)
	}
	if n := len(e.chunks(privateKey, "m1")); n != 0 {
		t.Fatalf("chunks after forgetting = %d, want none", n)
	}
	if hits, _ := e.searcher("m1").Search(ctx, privateKey, "草莓", search.Range{}, 5); len(hits) != 0 {
		t.Fatalf("hits after forgetting = %v", hits)
	}
	e.say(privateKey, "20001", "小明", "火锅", at(62))
	e.emb.take()
	e.run(e.indexer("m1", 16))
	if got := flat(e.emb.take()); !slices.Equal(got, []string{"[11-13 10:02] 小明：火锅"}) {
		t.Fatalf("after forgetting embedded %q, want only what came after", got)
	}
}

func TestIndexer_PrivateForgettingRebuildsTheChunkItCuts(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.say(privateKey, "20001", "小明", "我喜欢草莓", at(0))
	e.say(privateKey, "", "", "草莓很甜", at(2))
	e.say(privateKey, "20001", "小明", "猫", at(4))
	e.run(e.indexer("m1", 16))
	e.emb.take()
	if err := e.st.ForgetPrivate(ctx, privateKey, "20001", at(1)); err != nil {
		t.Fatal(err)
	}
	e.run(e.indexer("m1", 16))
	if got := flat(e.emb.take()); !slices.Equal(got, []string{"[11-13 09:02] 你：草莓很甜\n[11-13 09:04] 小明：猫"}) {
		t.Fatalf("rebuilt %q", got)
	}
}

func TestIndexer_ForgettingDuringARoundDiscardsIt(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.say(groupKey, "20002", "小红", "我养了一只猫", at(0))
	e.say(groupKey, "20001", "小明", "草莓", at(1))
	var once sync.Once
	e.emb.set(func(f *fakeEmbedder) {
		f.onInput = func() {
			once.Do(func() {
				if err := e.st.ForgetMember(ctx, groupKey, "20002", at(10)); err != nil {
					t.Error(err)
				}
			})
		}
	})
	e.run(e.indexer("m1", 16))
	if n := len(e.chunks(groupKey, "m1")); n != 0 || e.cursor(groupKey) != 0 {
		t.Fatalf("chunks = %d, cursor = %d after forgetting mid-round; want nothing written", n, e.cursor(groupKey))
	}
	e.emb.take()
	e.run(e.indexer("m1", 16))
	if got := flat(e.emb.take()); !slices.Equal(got, []string{"[11-13 09:01] 小明：草莓"}) {
		t.Fatalf("next round embedded %q", got)
	}
}

func TestIndexer_StartAndClose(t *testing.T) {
	e := newEnv(t)
	ix := e.indexer("m1", 16)
	ix.Close()
	ix = e.indexer("m1", 16)
	ix.Start()
	done := make(chan struct{})
	go func() {
		ix.Close()
		ix.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
}
