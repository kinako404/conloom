package usage

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func kolkata(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func utc(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, time.UTC)
}

func ptr(v int64) *int64 { return &v }

func summarizeInput() ([]store.UsageBucket, []store.LatencySample) {
	bucket := func(start time.Time, model string, calls, errs, prompt, completion, reasoning int64) store.UsageBucket {
		return store.UsageBucket{Start: start, BotInstanceID: "qq-main", Purpose: store.PurposeChat, Role: "short_context", Model: model,
			Calls: calls, Errors: errs, PromptTokens: prompt, CompletionTokens: completion, ReasoningTokens: reasoning}
	}
	buckets := []store.UsageBucket{
		bucket(utc(11, 18, 15), "m-a", 9, 9, 999, 999, 9), // 23:45 on the 11th, before the range
		bucket(utc(12, 18, 15), "m-a", 2, 1, 100, 10, 1),  // 23:45 on the 12th
		bucket(utc(12, 18, 30), "m-a", 1, 0, 50, 5, 0),    // 00:00 on the 13th
		bucket(utc(13, 5, 0), "m-b", 3, 0, 400, 40, 0),
		bucket(utc(14, 18, 30), "m-b", 7, 0, 7, 7, 7), // 00:00 on the 15th, after the range
	}
	sample := func(at time.Time, ms int64) store.LatencySample {
		return store.LatencySample{At: at, BotInstanceID: "qq-main", Purpose: store.PurposeChat, Role: "short_context", Model: "m-a", LatencyMS: ms}
	}
	samples := []store.LatencySample{
		sample(utc(12, 18, 20), 200),
		sample(utc(12, 18, 16), 100),
		sample(utc(12, 18, 31), 400),
		sample(utc(12, 18, 40), 300),
		sample(utc(14, 18, 31), 9000),
	}
	return buckets, samples
}

func TestSummarize_LocalDaysAndHalfHourZone(t *testing.T) {
	loc := kolkata(t)
	buckets, samples := summarizeInput()

	rep, err := Summarize(buckets, samples, loc, "2026-11-12", "2026-11-14", "model", true)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if rep.From != "2026-11-12" || rep.To != "2026-11-14" || rep.Group != "model" || rep.Timezone != "Asia/Kolkata" {
		t.Errorf("header = %+v", rep)
	}
	want := []Day{
		{Day: "2026-11-12", Rows: []Row{{Key: "m-a", Calls: 2, Errors: 1, PromptTokens: 100, CompletionTokens: 10, ReasoningTokens: 1,
			LatencyP50MS: ptr(100), LatencyP95MS: ptr(200)}}},
		{Day: "2026-11-13", Rows: []Row{
			{Key: "m-a", Calls: 1, PromptTokens: 50, CompletionTokens: 5, LatencyP50MS: ptr(300), LatencyP95MS: ptr(400)},
			{Key: "m-b", Calls: 3, PromptTokens: 400, CompletionTokens: 40},
		}},
		{Day: "2026-11-14", Rows: []Row{}},
	}
	if !reflect.DeepEqual(rep.Days, want) {
		t.Errorf("days =\n%s\nwant\n%s", show(rep.Days), show(want))
	}
	wantTotals := []Row{
		{Key: "m-b", Calls: 3, PromptTokens: 400, CompletionTokens: 40},
		{Key: "m-a", Calls: 3, Errors: 1, PromptTokens: 150, CompletionTokens: 15, ReasoningTokens: 1,
			LatencyP50MS: ptr(200), LatencyP95MS: ptr(400)},
	}
	if !reflect.DeepEqual(rep.Totals, wantTotals) {
		t.Errorf("totals =\n%s\nwant\n%s", show(rep.Totals), show(wantTotals))
	}
	raw := show(rep)
	for _, s := range []string{`"rows":[]`, `"calls":3`, `"errors":1`, `"prompt_tokens":150`, `"completion_tokens":15`,
		`"reasoning_tokens":1`, `"latency_p50_ms":200`, `"latency_p95_ms":400`, `"latency_p50_ms":null`, `"timezone":"Asia/Kolkata"`} {
		if !strings.Contains(raw, s) {
			t.Errorf("JSON lacks %s: %s", s, raw)
		}
	}

	rep, err = Summarize(buckets, samples, loc, "2026-11-12", "2026-11-14", "model", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rep.Days {
		for _, r := range d.Rows {
			if r.LatencyP50MS != nil || r.LatencyP95MS != nil {
				t.Errorf("percentiles off but %s %s has latency %v/%v", d.Day, r.Key, r.LatencyP50MS, r.LatencyP95MS)
			}
		}
	}
	if rep.Totals[1].LatencyP50MS != nil {
		t.Errorf("percentiles off but totals have latency")
	}

	empty, err := Summarize(nil, nil, loc, "2026-11-13", "2026-11-13", "bot", true)
	if err != nil {
		t.Fatal(err)
	}
	if raw := show(empty); !strings.Contains(raw, `"days":[{"day":"2026-11-13","rows":[]}],"totals":[]`) {
		t.Errorf("empty report = %s", raw)
	}
}

func TestSummarize_Groups(t *testing.T) {
	buckets, samples := summarizeInput()
	buckets[2].Purpose, buckets[2].Role, buckets[2].BotInstanceID = store.PurposeMemory, "long_context", "tg-main"
	for group, want := range map[string][]string{
		"role":    {"short_context", "long_context"},
		"purpose": {store.PurposeChat, store.PurposeMemory},
		"bot":     {"qq-main", "tg-main"},
	} {
		rep, err := Summarize(buckets, samples, time.UTC, "2026-11-12", "2026-11-13", group, false)
		if err != nil {
			t.Fatalf("%s: %v", group, err)
		}
		var keys []string
		for _, r := range rep.Totals {
			keys = append(keys, r.Key)
		}
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("%s totals = %v, want %v", group, keys, want)
		}
	}
}

func TestSummarize_RejectsBadInput(t *testing.T) {
	loc := kolkata(t)
	for _, c := range []struct{ from, to, group string }{
		{"2026-11-12", "2026-11-13", "user"},
		{"2026-11-12", "2026-11-13", ""},
		{"2026-11-14", "2026-11-13", "model"},
		{"2026-13-01", "2026-13-02", "model"},
		{"2026/11/12", "2026-11-13", "model"},
		{"2026-11-12", "", "model"},
		{"2026-11-12T00:00:00Z", "2026-11-13", "model"},
	} {
		if _, err := Summarize(nil, nil, loc, c.from, c.to, c.group, false); err == nil {
			t.Errorf("Summarize(%q, %q, %q) accepted", c.from, c.to, c.group)
		}
	}
}

func TestRange(t *testing.T) {
	start, end, err := Range(kolkata(t), "2026-11-12", "2026-11-14")
	if err != nil {
		t.Fatal(err)
	}
	if !start.Equal(utc(11, 18, 30)) || !end.Equal(utc(14, 18, 30)) {
		t.Errorf("Range = [%v, %v), want [%v, %v)", start.UTC(), end.UTC(), utc(11, 18, 30), utc(14, 18, 30))
	}
	if _, _, err := Range(time.UTC, "2026-11-14", "2026-11-12"); err == nil {
		t.Error("Range accepted from after to")
	}
	if _, _, err := Range(time.UTC, "2026-11-12", "2026-11-12"); err != nil {
		t.Errorf("Range of one day: %v", err)
	}
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(b)
}
