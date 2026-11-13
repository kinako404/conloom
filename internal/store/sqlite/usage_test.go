package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

var usageDay = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func usageCall(at time.Time, model, outcome string) store.ModelCall {
	return store.ModelCall{
		At: at, BotInstanceID: "qq-main", ConversationKey: testConvKey, TaskID: "task_1",
		Purpose: store.PurposeChat, Role: "short_context", Model: model,
		PromptTokens: 100, CompletionTokens: 20, ReasoningTokens: 5, EstimatedTokens: 90,
		LatencyMS: 1200, Outcome: outcome, FinishReason: "stop",
	}
}

func insertCalls(t *testing.T, db *DB, calls ...store.ModelCall) {
	t.Helper()
	if err := db.InsertModelCalls(context.Background(), calls); err != nil {
		t.Fatalf("InsertModelCalls: %v", err)
	}
}

func listCalls(t *testing.T, db *DB, f store.ModelCallFilter) []store.ModelCall {
	t.Helper()
	if f.Limit == 0 {
		f.Limit = 100
	}
	calls, err := db.ListModelCalls(context.Background(), f)
	if err != nil {
		t.Fatalf("ListModelCalls(%+v): %v", f, err)
	}
	return calls
}

func callIDs(calls []store.ModelCall) []int64 {
	ids := make([]int64, len(calls))
	for i, c := range calls {
		ids[i] = c.ID
	}
	return ids
}

func TestModelCalls_InsertAndList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first := usageCall(usageDay.Add(time.Minute), "model-a", store.OutcomeOK)
	second := usageCall(usageDay.Add(2*time.Hour), "model-b", store.OutcomeError)
	second.TaskID, second.Purpose, second.Role, second.FinishReason = "", store.PurposeMemory, "memory", ""
	third := store.ModelCall{
		At: usageDay.Add(5*time.Hour + 123456789), BotInstanceID: "qq-main", Purpose: store.PurposeEmbedIndex,
		Role: "embedding", Model: "embed-1", PromptTokens: 300, LatencyMS: 80, Outcome: store.OutcomeTimeout, ID: 999,
	}
	insertCalls(t, db, first, second, third)

	got, err := db.ListModelCalls(ctx, store.ModelCallFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListModelCalls: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListModelCalls(Limit 2) = %d calls, want 2", len(got))
	}
	if got[0].ID <= got[1].ID {
		t.Fatalf("IDs %v, want descending", callIDs(got))
	}
	third.ID, second.ID = got[0].ID, got[1].ID
	if !reflect.DeepEqual(got, []store.ModelCall{third, second}) {
		t.Fatalf("ListModelCalls(Limit 2) =\n%+v\nwant\n%+v", got, []store.ModelCall{third, second})
	}
	all := listCalls(t, db, store.ModelCallFilter{})
	if len(all) != 3 {
		t.Fatalf("all calls = %d, want 3", len(all))
	}
	first.ID = all[2].ID
	if !reflect.DeepEqual(all[2], first) {
		t.Fatalf("first call = %+v, want %+v", all[2], first)
	}
	if none, err := db.ListModelCalls(ctx, store.ModelCallFilter{}); err != nil || len(none) != 0 {
		t.Fatalf("ListModelCalls(Limit 0) = %v, %v; want none", none, err)
	}

	for _, c := range []struct {
		name string
		f    store.ModelCallFilter
		want []int64
	}{
		{"before id", store.ModelCallFilter{BeforeID: third.ID}, []int64{second.ID, first.ID}},
		{"task", store.ModelCallFilter{TaskID: "task_1"}, []int64{first.ID}},
		{"model", store.ModelCallFilter{Model: "model-b"}, []int64{second.ID}},
		{"purpose", store.ModelCallFilter{Purpose: store.PurposeEmbedIndex}, []int64{third.ID}},
		{"conversation", store.ModelCallFilter{ConversationKey: testConvKey}, []int64{second.ID, first.ID}},
		{"from", store.ModelCallFilter{From: usageDay.Add(2 * time.Hour)}, []int64{third.ID, second.ID}},
		{"to", store.ModelCallFilter{To: usageDay.Add(2 * time.Hour)}, []int64{first.ID}},
		{"from and to", store.ModelCallFilter{From: usageDay.Add(time.Hour), To: usageDay.Add(3 * time.Hour)}, []int64{second.ID}},
	} {
		if got := callIDs(listCalls(t, db, c.f)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: IDs = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestUsageBuckets_FifteenMinuteSlots(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	failed := usageCall(usageDay.Add(14*time.Minute), "model-a", store.OutcomeError)
	failed.PromptTokens, failed.CompletionTokens, failed.ReasoningTokens = 7, 0, 0
	otherModel := usageCall(usageDay.Add(3*time.Minute), "model-b", store.OutcomeOK)
	otherBot := usageCall(usageDay.Add(5*time.Minute), "model-a", store.OutcomeOK)
	otherBot.BotInstanceID = "tg-main"
	insertCalls(t, db,
		usageCall(usageDay.Add(time.Minute), "model-a", store.OutcomeOK),
		failed,
		usageCall(usageDay.Add(16*time.Minute), "model-a", store.OutcomeOK),
		otherModel,
		otherBot,
		usageCall(usageDay.Add(-time.Second), "model-a", store.OutcomeOK),
		usageCall(usageDay.Add(30*time.Minute), "model-a", store.OutcomeOK),
	)

	got, err := db.UsageBuckets(ctx, usageDay, usageDay.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("UsageBuckets: %v", err)
	}
	bucket := func(start time.Duration, bot, model string, calls, errs, prompt, completion, reasoning int64) store.UsageBucket {
		return store.UsageBucket{Start: usageDay.Add(start), BotInstanceID: bot, Purpose: store.PurposeChat,
			Role: "short_context", Model: model, Calls: calls, Errors: errs,
			PromptTokens: prompt, CompletionTokens: completion, ReasoningTokens: reasoning}
	}
	want := []store.UsageBucket{
		bucket(0, "qq-main", "model-a", 2, 1, 107, 20, 5),
		bucket(0, "qq-main", "model-b", 1, 0, 100, 20, 5),
		bucket(0, "tg-main", "model-a", 1, 0, 100, 20, 5),
		bucket(15*time.Minute, "qq-main", "model-a", 1, 0, 100, 20, 5),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UsageBuckets =\n%+v\nwant\n%+v", got, want)
	}
	if got[0].Start.Location() != time.UTC {
		t.Fatalf("Start location = %v, want UTC", got[0].Start.Location())
	}
}

func TestLatencySamples_OkOnly(t *testing.T) {
	db := newTestDB(t)
	ok := usageCall(usageDay.Add(time.Minute), "model-a", store.OutcomeOK)
	ok.LatencyMS = 850
	insertCalls(t, db, ok,
		usageCall(usageDay.Add(2*time.Minute), "model-a", store.OutcomeError),
		usageCall(usageDay.Add(3*time.Minute), "model-a", store.OutcomeTimeout),
		usageCall(usageDay.Add(time.Hour), "model-a", store.OutcomeOK),
	)
	got, err := db.LatencySamples(context.Background(), usageDay, usageDay.Add(time.Hour))
	if err != nil {
		t.Fatalf("LatencySamples: %v", err)
	}
	want := []store.LatencySample{{At: ok.At, BotInstanceID: "qq-main", Purpose: store.PurposeChat,
		Role: "short_context", Model: "model-a", LatencyMS: 850}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LatencySamples = %+v, want %+v", got, want)
	}
}

func TestUsageQueries_UseSlotIndex(t *testing.T) {
	db := newTestDB(t)
	for name, query := range map[string]string{"buckets": usageBucketsQuery, "latencies": latencySamplesQuery} {
		rows, err := db.r.Query("EXPLAIN QUERY PLAN "+query, slotArgs(usageDay, usageDay.Add(30*day))...)
		if err != nil {
			t.Fatalf("explain %s: %v", name, err)
		}
		var plan []string
		for rows.Next() {
			var detail string
			if err := rows.Scan(new(int), new(int), new(int), &detail); err != nil {
				t.Fatalf("scan %s plan: %v", name, err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read %s plan: %v", name, err)
		}
		rows.Close()
		want := []string{"SEARCH model_calls USING COVERING INDEX model_calls_slot (<expr>>? AND <expr><?)"}
		if !reflect.DeepEqual(plan, want) {
			t.Errorf("%s query plan = %q, want %q", name, plan, want)
		}
	}
}

func TestPurgeModelCalls_Batches(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cutoff := usageDay
	for i := range 5 {
		insertCalls(t, db, usageCall(cutoff.Add(-time.Duration(i+1)*day), "model-a", store.OutcomeOK))
	}
	fresh := usageCall(cutoff, "model-a", store.OutcomeOK)
	insertCalls(t, db, fresh)
	for _, want := range []int{2, 2, 1, 0} {
		if n, err := db.PurgeModelCalls(ctx, cutoff, 2); err != nil || n != want {
			t.Fatalf("PurgeModelCalls = %d, %v; want %d", n, err, want)
		}
	}
	left := listCalls(t, db, store.ModelCallFilter{})
	if len(left) != 1 || !left[0].At.Equal(fresh.At) {
		t.Fatalf("calls left = %+v, want only the fresh one", left)
	}
	if n, err := db.PurgeModelCalls(ctx, cutoff.Add(day), 0); err != nil || n != 0 {
		t.Fatalf("PurgeModelCalls(limit 0) = %d, %v; want 0", n, err)
	}
}
