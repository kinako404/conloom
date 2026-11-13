package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestUsage_DefaultsAndValidation(t *testing.T) {
	h := newOverviewHarness(t, nil)
	h.insertCalls(usageTestCall(h.now.Add(-time.Hour), "qwen3", 100, 10))

	var rep usage.Report
	h.getJSON("/panel/api/usage", &rep)
	if rep.From != "2026-11-07" || rep.To != "2026-11-13" || rep.Group != "model" || rep.Timezone != "UTC+8" || len(rep.Days) != 7 {
		t.Errorf("default report = %s..%s group %q tz %q, %d days; want the 7 days to 2026-11-13 by model in UTC+8",
			rep.From, rep.To, rep.Group, rep.Timezone, len(rep.Days))
	}
	if len(rep.Totals) != 1 || rep.Totals[0].Key != "qwen3" || rep.Totals[0].LatencyP50MS == nil {
		t.Errorf("totals = %+v, want qwen3 with latencies", rep.Totals)
	}

	h.getJSON("/panel/api/usage?to=2026-11-01&group=purpose", &rep)
	if rep.From != "2026-10-26" || rep.To != "2026-11-01" || rep.Group != "purpose" {
		t.Errorf("to only: %s..%s by %s, want 2026-10-26..2026-11-01 by purpose", rep.From, rep.To, rep.Group)
	}
	h.getJSON("/panel/api/usage?from=2026-11-10", &rep)
	if rep.From != "2026-11-10" || rep.To != "2026-11-13" {
		t.Errorf("from only: %s..%s, want 2026-11-10..2026-11-13", rep.From, rep.To)
	}

	for _, query := range []string{
		"from=2026-11-14&to=2026-11-13",
		"from=2026/11/01&to=2026-11-13",
		"from=2026-11-01&to=13-11-2026",
		"to=tomorrow",
		"from=2025-10-09&to=2026-11-13", // 401 days
		"from=2024-01-01&to=2026-11-13",
		"group=user",
		"group=MODEL",
	} {
		errorBody(t, h.get("/panel/api/usage?"+query), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}

	h.getJSON("/panel/api/usage?from=2025-10-10&to=2026-11-13", &rep)
	if len(rep.Days) != 400 {
		t.Errorf("400-day range has %d days", len(rep.Days))
	}
	if rep.Totals[0].LatencyP50MS != nil || rep.Totals[0].LatencyP95MS != nil {
		t.Errorf("400-day range: latencies %v %v, want null", rep.Totals[0].LatencyP50MS, rep.Totals[0].LatencyP95MS)
	}
	h.getJSON("/panel/api/usage?from=2026-08-15&to=2026-11-13", &rep) // 91 days
	if len(rep.Days) != 91 || rep.Totals[0].LatencyP50MS != nil {
		t.Errorf("91-day range: %d days, p50 %v; want null latencies", len(rep.Days), rep.Totals[0].LatencyP50MS)
	}
	h.getJSON("/panel/api/usage?from=2026-08-16&to=2026-11-13", &rep) // 90 days
	if len(rep.Days) != 90 || rep.Totals[0].LatencyP50MS == nil || *rep.Totals[0].LatencyP50MS != 1200 {
		t.Errorf("90-day range: %d days, p50 %v; want 1200", len(rep.Days), rep.Totals[0].LatencyP50MS)
	}
	w := h.get("/panel/api/usage?from=2026-08-15&to=2026-11-13")
	var body struct {
		Totals []map[string]json.RawMessage `json:"totals"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body.Totals[0]["latency_p95_ms"]); got != "null" {
		t.Errorf("91-day latency_p95_ms = %s, want null", got)
	}
}

func TestUsage_TodayIncluded(t *testing.T) {
	h := newOverviewHarness(t, nil)
	h.insertCalls(usageTestCall(h.now, "qwen3", 100, 10))

	for _, path := range []string{"/panel/api/usage", "/panel/api/usage?from=2026-11-13&to=2026-11-13&group=bot"} {
		var rep usage.Report
		h.getJSON(path, &rep)
		last := rep.Days[len(rep.Days)-1]
		if last.Day != "2026-11-13" || len(last.Rows) != 1 || last.Rows[0].Calls != 1 || last.Rows[0].PromptTokens != 100 {
			t.Errorf("%s: last day = %+v, want today with the call", path, last)
		}
	}
}

type callsBody struct {
	Calls []struct {
		ID    int64  `json:"id"`
		Model string `json:"model"`
	} `json:"calls"`
	NextBefore *int64 `json:"next_before"`
}

func (b callsBody) ids() []int64 {
	var out []int64
	for _, c := range b.Calls {
		out = append(out, c.ID)
	}
	return out
}

func TestUsageCalls_PagingAndFilters(t *testing.T) {
	h := newOverviewHarness(t, nil)
	day := func(d, hour int) time.Time { return time.Date(2026, 11, d, hour, 0, 0, 0, overviewZone) }
	memory := usageTestCall(day(12, 9), "qwen3", 400, 40)
	memory.Purpose, memory.TaskID = store.PurposeMemory, ""
	other := usageTestCall(day(13, 10), "deepseek", 300, 30)
	other.ConversationKey = "qq-main:qq:private:20001"
	reasoning := usageTestCall(day(13, 11), "deepseek", 500, 50)
	reasoning.ReasoningTokens, reasoning.EstimatedTokens, reasoning.Outcome, reasoning.FinishReason = 20, 480, store.OutcomeError, ""
	h.insertCalls(
		usageTestCall(day(11, 23), "qwen3", 100, 10), // id 1
		memory, // id 2
		usageTestCall(day(12, 23), "qwen3", 200, 20), // id 3
		other,     // id 4
		reasoning, // id 5
	)

	var b callsBody
	for _, tc := range []struct {
		query string
		ids   []int64
		next  int64
	}{
		{"limit=2", []int64{5, 4}, 4},
		{"limit=2&before=4", []int64{3, 2}, 2},
		{"limit=2&before=2", []int64{1}, 0},
		{"limit=2&before=3", []int64{2, 1}, 0},
		{"", []int64{5, 4, 3, 2, 1}, 0},
		{"model=qwen3", []int64{3, 2, 1}, 0},
		{"purpose=memory", []int64{2}, 0},
		{"conversation=qq-main:qq:private:20001", []int64{4}, 0},
		{"from=2026-11-12&to=2026-11-12", []int64{3, 2}, 0},
		{"from=2026-11-13", []int64{5, 4}, 0},
		{"to=2026-11-11", []int64{1}, 0},
		{"model=deepseek&from=2026-11-13&limit=1", []int64{5}, 5},
	} {
		h.getJSON("/panel/api/usage/calls?"+tc.query, &b)
		if fmt.Sprint(b.ids()) != fmt.Sprint(tc.ids) || (b.NextBefore == nil) != (tc.next == 0) ||
			(b.NextBefore != nil && *b.NextBefore != tc.next) {
			t.Errorf("%q: ids %v next %v, want %v next %d", tc.query, b.ids(), b.NextBefore, tc.ids, tc.next)
		}
	}

	var full struct {
		Calls []map[string]any `json:"calls"`
	}
	h.getJSON("/panel/api/usage/calls?limit=1", &full)
	want := map[string]any{
		"id": 5.0, "at": "2026-11-13T03:00:00Z", "bot_instance_id": "qq-main", "conversation_key": "qq-main:qq:group:10001",
		"task_id": "task_1", "purpose": "chat", "role": "short_context", "model": "deepseek", "prompt_tokens": 500.0,
		"completion_tokens": 50.0, "reasoning_tokens": 20.0, "estimated_tokens": 480.0, "latency_ms": 1200.0,
		"outcome": "error", "finish_reason": "",
	}
	if got := full.Calls[0]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("call = %v\nwant %v", got, want)
	}

	w := h.get("/panel/api/usage/calls?model=none")
	if w.Code != http.StatusOK || w.Body.String() != "{\"calls\":[],\"next_before\":null}\n" {
		t.Errorf("no calls: %d %s", w.Code, w.Body)
	}

	for _, query := range []string{
		"limit=0", "limit=x", "before=0", "before=-1", "before=x", "from=2026-13-01", "to=yesterday",
		"from=2026-11-13&to=2026-11-12",
	} {
		errorBody(t, h.get("/panel/api/usage/calls?"+query), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
}

func TestUsageCalls_LimitCapped(t *testing.T) {
	h := newOverviewHarness(t, nil)
	calls := make([]store.ModelCall, 205)
	for i := range calls {
		calls[i] = usageTestCall(h.now.Add(-time.Duration(i)*time.Minute), "qwen3", 10, 1)
	}
	h.insertCalls(calls...)

	var b callsBody
	h.getJSON("/panel/api/usage/calls?limit=500", &b)
	if len(b.Calls) != 200 || b.NextBefore == nil || *b.NextBefore != 6 {
		t.Errorf("limit=500: %d calls, next %v; want 200 and next_before 6", len(b.Calls), b.NextBefore)
	}
	h.getJSON("/panel/api/usage/calls", &b)
	if len(b.Calls) != 50 || b.NextBefore == nil || *b.NextBefore != 156 {
		t.Errorf("default limit: %d calls, next %v; want 50 and next_before 156", len(b.Calls), b.NextBefore)
	}
}
