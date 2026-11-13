package sqlite

import (
	"context"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

type benchCallKind struct{ purpose, role, model string }

// benchConfigured is the calls of a bot whose roles each use one model,
// mostly chat. benchSpread draws purpose, role and model independently,
// so most 15-minute buckets hold a single call.
var (
	benchConfigured = []benchCallKind{
		{store.PurposeChat, "short_context", "model-a"}, {store.PurposeChat, "short_context", "model-a"},
		{store.PurposeChat, "short_context", "model-a"}, {store.PurposeChat, "short_context", "model-a"},
		{store.PurposeChat, "long_context", "model-b"}, {store.PurposeMemory, "memory", "model-a"},
		{store.PurposeEmbedIndex, "embedding", "embed-1"}, {store.PurposeEmbedIndex, "embedding", "embed-1"},
		{store.PurposeEmbedQuery, "embedding", "embed-1"},
	}
	benchSpread = func() []benchCallKind {
		var kinds []benchCallKind
		for _, p := range []string{store.PurposeChat, store.PurposeMemory, store.PurposeEmbedIndex, store.PurposeEmbedQuery} {
			for _, r := range []string{"short_context", "long_context", "embedding"} {
				for _, m := range []string{"model-a", "model-b", "embed-1"} {
					kinds = append(kinds, benchCallKind{p, r, m})
				}
			}
		}
		return kinds
	}()
)

// benchModelCalls is n calls of kinds spread evenly over days days ending at
// end, from two bots.
func benchModelCalls(n, days int, end time.Time, kinds []benchCallKind) []store.ModelCall {
	rng := rand.New(rand.NewPCG(1, 1))
	bots := []string{"qq-main", "tg-main"}
	outcomes := []string{store.OutcomeOK, store.OutcomeOK, store.OutcomeOK, store.OutcomeOK, store.OutcomeOK,
		store.OutcomeOK, store.OutcomeOK, store.OutcomeOK, store.OutcomeError, store.OutcomeTimeout}
	start := end.Add(-time.Duration(days) * day)
	step := time.Duration(days) * day / time.Duration(n)
	calls := make([]store.ModelCall, n)
	for i := range calls {
		k := kinds[rng.IntN(len(kinds))]
		calls[i] = store.ModelCall{
			At: start.Add(time.Duration(i) * step), BotInstanceID: bots[rng.IntN(len(bots))],
			ConversationKey: testConvKey, TaskID: "task_1", Purpose: k.purpose, Role: k.role, Model: k.model,
			PromptTokens: rng.IntN(4000), CompletionTokens: rng.IntN(500), ReasoningTokens: rng.IntN(200),
			EstimatedTokens: rng.IntN(4000), LatencyMS: int64(200 + rng.IntN(5000)),
			Outcome: outcomes[rng.IntN(len(outcomes))], FinishReason: "stop",
		}
	}
	return calls
}

// BenchmarkUsageBuckets30d reads the 15-minute buckets and the sorted
// latencies of 30 days (about 75,000 calls) from a table of 1,000,000 calls
// spread over 400 days.
func BenchmarkUsageBuckets30d(b *testing.B) {
	for _, shape := range []struct {
		name  string
		kinds []benchCallKind
	}{{"configured", benchConfigured}, {"spread", benchSpread}} {
		b.Run(shape.name, func(b *testing.B) { benchUsage30d(b, shape.kinds) })
	}
}

func benchUsage30d(b *testing.B, kinds []benchCallKind) {
	ctx := context.Background()
	db, err := Open(ctx, b.TempDir()+"/bench.db")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	end := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	for chunk := range slices.Chunk(benchModelCalls(1_000_000, 400, end, kinds), 10_000) {
		if err := db.InsertModelCalls(ctx, chunk); err != nil {
			b.Fatal(err)
		}
	}
	from := end.Add(-30 * day)
	buckets := func(b *testing.B) {
		got, err := db.UsageBuckets(ctx, from, end)
		if err != nil || len(got) == 0 {
			b.Fatalf("UsageBuckets = %d buckets, %v", len(got), err)
		}
	}
	latencies := func(b *testing.B) {
		samples, err := db.LatencySamples(ctx, from, end)
		if err != nil || len(samples) < 50_000 {
			b.Fatalf("LatencySamples = %d samples, %v", len(samples), err)
		}
		ms := make([]int64, len(samples))
		for i, s := range samples {
			ms[i] = s.LatencyMS
		}
		slices.Sort(ms)
	}
	b.Run("buckets", func(b *testing.B) {
		for b.Loop() {
			buckets(b)
		}
	})
	b.Run("latencies", func(b *testing.B) {
		for b.Loop() {
			latencies(b)
		}
	})
	b.Run("both", func(b *testing.B) {
		for b.Loop() {
			buckets(b)
			latencies(b)
		}
	})
}
