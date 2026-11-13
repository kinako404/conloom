package vector

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestSearcher_RanksByCosine(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	cat := e.say(groupKey, "20001", "小明", "我养了一只猫", at(0))
	e.say(groupKey, "20002", "小红", "猫猫好可爱", at(1))
	berry := e.say(groupKey, "20001", "小明", "我喜欢草莓", at(60))
	trip := e.say(groupKey, "20002", "小红", "下个月去旅行", at(120))
	e.run(e.indexer("m1", 16))

	s := e.searcher("m1")
	hits, err := s.Search(ctx, groupKey, "草莓", search.Range{}, 2)
	if err != nil || len(hits) != 2 {
		t.Fatalf("hits = %v, %v", hits, err)
	}
	if hits[0].FromSeq != berry || hits[0].ToSeq != berry || hits[0].Score < 0.99 || hits[1].Score >= hits[0].Score {
		t.Errorf("hits = %+v, want the 草莓 chunk first", hits)
	}
	hits, _ = s.Search(ctx, groupKey, "猫", search.Range{}, 10)
	if len(hits) != 3 || hits[0].FromSeq != cat || hits[0].ToSeq != cat+1 {
		t.Errorf("hits = %+v, want the 猫 chunk first", hits)
	}
	if hits, _ := s.Search(ctx, group2Key, "猫", search.Range{}, 10); len(hits) != 0 {
		t.Errorf("another conversation's hits = %v", hits)
	}
	for _, c := range []struct {
		r    search.Range
		want []int64
	}{
		{search.Range{After: at(30)}, []int64{berry, trip}},
		{search.Range{Before: at(60)}, []int64{cat}},
		{search.Range{After: at(1), Before: at(60)}, []int64{cat}},
		{search.Range{Before: at(60).Add(time.Second)}, []int64{cat, berry}},
		{search.Range{Before: at(120)}, []int64{cat, berry}},
		{search.Range{After: at(60), Before: at(61)}, []int64{berry}},
		{search.Range{After: at(61), Before: at(119)}, nil},
	} {
		hits, _ := s.Search(ctx, groupKey, "旅行", c.r, 10)
		var got []int64
		for _, h := range hits {
			got = append(got, h.FromSeq)
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("hits in %+v start at %v, want %v", c.r, got, c.want)
		}
	}
	if hits, _ := s.Search(ctx, groupKey, "  ", search.Range{}, 10); hits != nil {
		t.Errorf("blank query hits = %v", hits)
	}
	if hits, _ := s.Search(ctx, groupKey, "猫", search.Range{}, 0); hits != nil {
		t.Errorf("limit 0 hits = %v", hits)
	}
	if n := e.metric("conloom_search_seconds", "vector"); n != 12 {
		t.Errorf("search_seconds{vector} samples = %v, want 12", n)
	}
}

func TestSearcher_MinScore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.say(groupKey, "20001", "小明", "我养了一只猫", at(0))
	berry := e.say(groupKey, "20001", "小明", "我喜欢草莓", at(60))
	e.run(e.indexer("m1", 16))

	all, err := e.searcher("m1").Search(ctx, groupKey, "草莓", search.Range{}, 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("hits without a floor = %v, %v", all, err)
	}
	s := NewSearcher(SearcherDeps{Store: e.st, Embedder: e.emb.client("m1", 16), MinScore: 0.5})
	hits, err := s.Search(ctx, groupKey, "草莓", search.Range{}, 10)
	if err != nil || len(hits) != 1 || hits[0].FromSeq != berry {
		t.Fatalf("hits with MinScore 0.5 = %+v, %v; want only the 草莓 chunk", hits, err)
	}
	if hits, _ := s.Search(ctx, groupKey, "旅行", search.Range{}, 10); len(hits) != 0 {
		t.Fatalf("unrelated query hits = %+v, want none", hits)
	}
}

func TestSearcher_EmptyWhenTheServiceFails(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.say(groupKey, "20001", "小明", "我喜欢草莓", at(0))
	e.run(e.indexer("m1", 16))

	var logs bytes.Buffer
	s := NewSearcher(SearcherDeps{Store: e.st, Embedder: e.emb.client("m1", 16), QueryTimeout: 50 * time.Millisecond,
		Metrics: e.metrics, Logger: slog.New(slog.NewTextHandler(&logs, nil))})

	e.emb.set(func(f *fakeEmbedder) { f.delay = 2 * time.Second })
	start := time.Now()
	hits, err := s.Search(ctx, groupKey, "秘密草莓", search.Range{}, 5)
	if err != nil || hits != nil {
		t.Fatalf("Search on a slow service = %v, %v; want no hits and no error", hits, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Search took %s, want it bounded by the 50ms query timeout", d)
	}
	if e.requests("timeout") != 1 {
		t.Errorf("timeout requests = %v, want 1", e.requests("timeout"))
	}

	e.emb.set(func(f *fakeEmbedder) { f.delay, f.fail = 0, true })
	hits, err = s.Search(ctx, groupKey, "秘密草莓", search.Range{}, 5)
	if err != nil || hits != nil {
		t.Fatalf("Search on a failing service = %v, %v", hits, err)
	}
	if e.requests("error") != 1 {
		t.Errorf("error requests = %v, want 1", e.requests("error"))
	}
	if !strings.Contains(logs.String(), "timeout") || strings.Count(logs.String(), "level=WARN") != 2 {
		t.Errorf("logs = %s, want two warnings", logs.String())
	}
	if strings.Contains(logs.String(), "秘密") {
		t.Errorf("logs quote the query: %s", logs.String())
	}
	if n := e.metric("conloom_search_seconds", "vector"); n != 2 {
		t.Errorf("search_seconds{vector} samples = %v, want 2", n)
	}

	e.emb.set(func(f *fakeEmbedder) { f.fail, f.invalid = false, true })
	hits, err = s.Search(ctx, groupKey, "秘密草莓", search.Range{}, 5)
	if err != nil || hits != nil {
		t.Fatalf("Search with an invalid query vector = %v, %v", hits, err)
	}
	if e.requests("error") != 2 {
		t.Errorf("error requests = %v, want 2", e.requests("error"))
	}

	e.emb.set(func(f *fakeEmbedder) { f.invalid = false })
	s.now = func() time.Time { return time.Now().Add(breakerCooldown) }
	if hits, _ := s.Search(ctx, groupKey, "草莓", search.Range{}, 5); len(hits) != 1 {
		t.Errorf("hits after the service came back = %v", hits)
	}
}

func TestSearcher_CacheFollowsNewChunks(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.say(groupKey, "20001", "小明", "我喜欢草莓", at(0))
	e.run(e.indexer("m1", 16))
	s := e.searcher("m1")
	if hits, _ := s.Search(ctx, groupKey, "火锅", search.Range{}, 5); len(hits) != 1 {
		t.Fatalf("hits = %v", hits)
	}
	hotpot := e.say(groupKey, "20002", "小红", "晚上吃火锅", at(60))
	e.run(e.indexer("m1", 16))
	hits, _ := s.Search(ctx, groupKey, "火锅", search.Range{}, 5)
	if len(hits) != 2 || hits[0].FromSeq != hotpot {
		t.Fatalf("hits after a new chunk = %+v, want it first", hits)
	}
	if err := e.st.ForgetMember(ctx, groupKey, "20002", at(90)); err != nil {
		t.Fatal(err)
	}
	e.say(groupKey, "20001", "小明", "工作好累", at(120))
	e.run(e.indexer("m1", 16))
	hits, _ = s.Search(ctx, groupKey, "火锅", search.Range{}, 5)
	for _, h := range hits {
		if h.FromSeq == hotpot {
			t.Fatalf("hits after forgetting and a new chunk = %+v, still shows the forgotten chunk", hits)
		}
	}
	if len(hits) != 2 {
		t.Errorf("hits = %+v, want the two remaining chunks", hits)
	}
}

func TestSearcher_KeepsEightConversations(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var keys []string
	for i := range 9 {
		key := fmt.Sprintf("bot_test:qq:%s:%d", conloomapi.ConvGroup, 10001+i)
		keys = append(keys, key)
		e.say(key, "20001", "小明", "草莓", at(0))
	}
	e.run(e.indexer("m1", 16))
	s := e.searcher("m1")
	for _, key := range keys {
		if hits, _ := s.Search(ctx, key, "草莓", search.Range{}, 1); len(hits) != 1 {
			t.Fatalf("%s hits = %v", key, hits)
		}
	}
	got := s.cachedKeys()
	want := slices.Clone(keys[1:])
	slices.Reverse(want)
	if !slices.Equal(got, want) {
		t.Fatalf("cached = %v, want the last 8 searched, newest first", got)
	}
	if hits, _ := s.Search(ctx, keys[0], "草莓", search.Range{}, 1); len(hits) != 1 {
		t.Fatal("evicted conversation not found again")
	}
	if got := s.cachedKeys(); len(got) != 8 || got[0] != keys[0] || slices.Contains(got, keys[1]) {
		t.Errorf("cached = %v", got)
	}
}

func TestSearcher_BreakerSkipsTheServiceAfterFailures(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.say(groupKey, "20001", "小明", "我喜欢草莓", at(0))
	e.run(e.indexer("m1", 16))
	s := e.searcher("m1")
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	search1 := func() ([]search.Hit, int) {
		t.Helper()
		before := e.emb.requestCount()
		hits, err := s.Search(ctx, groupKey, "草莓", search.Range{}, 5)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		return hits, e.emb.requestCount() - before
	}

	e.emb.set(func(f *fakeEmbedder) { f.fail = true })
	for i := range breakerFailures - 1 {
		if _, n := search1(); n != 1 {
			t.Fatalf("failure %d sent %d requests, want 1", i+1, n)
		}
	}
	e.emb.set(func(f *fakeEmbedder) { f.fail = false })
	if hits, n := search1(); n != 1 || len(hits) != 1 {
		t.Fatalf("success after %d failures = %v hits, %d requests", breakerFailures-1, hits, n)
	}
	e.emb.set(func(f *fakeEmbedder) { f.fail = true })
	for range breakerFailures {
		if _, n := search1(); n != 1 {
			t.Fatalf("a success resets the count: %d requests, want 1", n)
		}
	}
	errors := e.requests("error")

	now = now.Add(breakerCooldown - time.Second)
	if hits, n := search1(); n != 0 || hits != nil {
		t.Fatalf("while open = %v hits, %d requests; want none", hits, n)
	}
	now = now.Add(time.Second)
	if _, n := search1(); n != 1 {
		t.Fatalf("after the cooldown sent %d requests, want one trial", n)
	}
	if _, n := search1(); n != 0 {
		t.Fatalf("after a failed trial sent %d requests, want the breaker open again", n)
	}
	if got := e.requests("error"); got != errors+1 {
		t.Errorf("error requests = %v, want %v (skipped searches are not requests)", got, errors+1)
	}

	now = now.Add(breakerCooldown)
	e.emb.set(func(f *fakeEmbedder) { f.fail, f.delay = false, 300*time.Millisecond })
	done := make(chan []search.Hit)
	base := e.emb.requestCount()
	go func() {
		hits, _ := s.Search(ctx, groupKey, "草莓", search.Range{}, 5)
		done <- hits
	}()
	deadline := time.Now().Add(5 * time.Second)
	for e.emb.requestCount() == base {
		if time.Now().After(deadline) {
			t.Fatal("the trial request never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if hits, n := search1(); n != 0 || hits != nil {
		t.Fatalf("during the trial = %v hits, %d requests; want none", hits, n)
	}
	if hits := <-done; len(hits) != 1 {
		t.Fatalf("trial hits = %v", hits)
	}
	e.emb.set(func(f *fakeEmbedder) { f.delay = 0 })
	if hits, n := search1(); n != 1 || len(hits) != 1 {
		t.Fatalf("after a successful trial = %v hits, %d requests", hits, n)
	}
}
