package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
)

// overviewZone is the server time zone of overviewHarness: 08:00 UTC on
// 2026-11-13, the harness's now, is 16:00 there.
var overviewZone = time.FixedZone("UTC+8", 8*3600)

// overviewHarness is a harness whose server reads a real SQLite store, live
// metrics from gatherer and bot presence from presence, logged in as testUser.
type overviewHarness struct {
	*harness
	db       *sqlite.DB
	gatherer *prometheus.Registry
	presence *obs.Presence
	cookie   *http.Cookie
}

// newOverviewHarness serves the bots qq-main and tg-main in overviewZone; mutate,
// when not nil, adjusts the dependencies before the server is built.
func newOverviewHarness(t *testing.T, mutate func(*Deps)) *overviewHarness {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h := &overviewHarness{harness: newHarness(t, nil), db: db, gatherer: prometheus.NewRegistry(), presence: obs.NewPresence()}
	h.cfg.Bots = []config.BotConfig{
		{ID: "qq-main", Platform: "qq", Name: "小织"},
		{ID: "tg-main", Platform: "telegram", Name: "织语"},
	}
	d := h.srv.d
	d.Store, d.Gatherer, d.Presence, d.Location = db, h.gatherer, h.presence, overviewZone
	d.Version, d.Commit, d.StartedAt = "v0.9.0", "abc1234", h.now.Add(-time.Hour)
	if mutate != nil {
		mutate(&d)
	}
	h.srv = New(d)
	h.h = h.srv.Handler()
	h.cookie, _ = h.loginOK()
	return h
}

func (h *overviewHarness) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodGet, path, "", withCookie(h.cookie))
}

// getJSON GETs path, which must answer 200, and decodes the body into v.
func (h *overviewHarness) getJSON(path string, v any) {
	h.t.Helper()
	w := h.get(path)
	if w.Code != http.StatusOK {
		h.t.Fatalf("GET %s: status %d, body %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		h.t.Fatalf("GET %s: decode %s: %v", path, w.Body, err)
	}
}

func (h *overviewHarness) insertCalls(calls ...store.ModelCall) {
	h.t.Helper()
	if err := h.db.InsertModelCalls(context.Background(), calls); err != nil {
		h.t.Fatalf("insert model calls: %v", err)
	}
}

// usageTestCall is a successful chat call of qq-main at at.
func usageTestCall(at time.Time, model string, prompt, completion int) store.ModelCall {
	return store.ModelCall{
		At: at, BotInstanceID: "qq-main", ConversationKey: "qq-main:qq:group:10001", TaskID: "task_1",
		Purpose: store.PurposeChat, Role: "short_context", Model: model,
		PromptTokens: prompt, CompletionTokens: completion, LatencyMS: 1200, Outcome: store.OutcomeOK, FinishReason: "stop",
	}
}

type overviewBody struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	StartedAt string `json:"started_at"`
	Timezone  string `json:"timezone"`
	Bots      []struct {
		ID         string  `json:"id"`
		Platform   string  `json:"platform"`
		Name       string  `json:"name"`
		LastSeenAt *string `json:"last_seen_at"`
	} `json:"bots"`
	Live struct {
		QueueDepth           *int64   `json:"queue_depth"`
		OutboxPending        *int64   `json:"outbox_pending"`
		EmbeddingBacklog     *int64   `json:"embedding_backlog"`
		FTSBackfillRemaining *int64   `json:"fts_backfill_remaining"`
		BreakersOpen         []string `json:"breakers_open"`
	} `json:"live"`
	Today map[string]int64 `json:"today"`
}

func TestOverview_EmptyInstall(t *testing.T) {
	h := newOverviewHarness(t, nil)
	queue := prometheus.NewGauge(prometheus.GaugeOpts{Name: "conloom_queue_depth", Help: "Tasks waiting."})
	breakers := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "conloom_breaker_open", Help: "Open breakers."}, []string{"role"})
	h.gatherer.MustRegister(queue, breakers)

	w := h.get("/panel/api/overview")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", w.Code, w.Body)
	}
	var live struct {
		Live map[string]json.RawMessage `json:"live"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"queue_depth": "0", "outbox_pending": "0", "embedding_backlog": "null", "fts_backfill_remaining": "null", "breakers_open": "[]",
	} {
		if got := string(live.Live[key]); got != want {
			t.Errorf("live.%s = %s, want %s", key, got, want)
		}
	}

	var o overviewBody
	h.getJSON("/panel/api/overview", &o)
	if o.Version != "v0.9.0" || o.Commit != "abc1234" || o.StartedAt != "2026-11-13T07:00:00Z" || o.Timezone != "UTC+8" {
		t.Errorf("header = %q %q %q %q", o.Version, o.Commit, o.StartedAt, o.Timezone)
	}
	if len(o.Bots) != 2 || o.Bots[0].ID != "qq-main" || o.Bots[0].Platform != "qq" || o.Bots[0].Name != "小织" ||
		o.Bots[1].ID != "tg-main" || o.Bots[0].LastSeenAt != nil || o.Bots[1].LastSeenAt != nil {
		t.Errorf("bots = %+v, want qq-main and tg-main never seen", o.Bots)
	}
	want := map[string]int64{"calls": 0, "errors": 0, "prompt_tokens": 0, "completion_tokens": 0, "reasoning_tokens": 0}
	if len(o.Today) != len(want) {
		t.Errorf("today = %v, want %v", o.Today, want)
	}
	for k, v := range want {
		if got, ok := o.Today[k]; !ok || got != v {
			t.Errorf("today.%s = %d (present %v), want %d", k, got, ok, v)
		}
	}
}

func TestOverview_LiveFromRegistry(t *testing.T) {
	h := newOverviewHarness(t, nil)
	m := obs.NewMetrics(h.gatherer)
	m.BreakerOpen.WithLabelValues("short_context").Set(1)
	m.BreakerOpen.WithLabelValues("long_context").Set(0)
	m.QueueDepth.Set(2)
	m.EmbeddingBacklog.Set(5)
	m.FTSBackfillRemaining.Set(0)
	h.presence.Seen("qq-main", time.Date(2026, 11, 13, 7, 59, 30, 0, overviewZone))

	// 2026-11-13 in overviewZone runs from 16:00 UTC on the 12th to 16:00 UTC on the 13th.
	errCall := usageTestCall(time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC), "qwen3", 50, 0)
	errCall.Outcome = store.OutcomeTimeout
	reasoning := usageTestCall(time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC), "deepseek", 1000, 300)
	reasoning.ReasoningTokens = 120
	h.insertCalls(
		usageTestCall(time.Date(2026, 11, 12, 15, 59, 59, 0, time.UTC), "qwen3", 9000, 9000),
		reasoning,
		usageTestCall(time.Date(2026, 11, 13, 7, 30, 0, 0, time.UTC), "qwen3", 200, 30),
		errCall,
	)

	var o overviewBody
	h.getJSON("/panel/api/overview", &o)
	if o.Live.QueueDepth == nil || *o.Live.QueueDepth != 2 {
		t.Errorf("queue_depth = %v, want 2", o.Live.QueueDepth)
	}
	if o.Live.EmbeddingBacklog == nil || *o.Live.EmbeddingBacklog != 5 {
		t.Errorf("embedding_backlog = %v, want 5", o.Live.EmbeddingBacklog)
	}
	if o.Live.FTSBackfillRemaining == nil || *o.Live.FTSBackfillRemaining != 0 {
		t.Errorf("fts_backfill_remaining = %v, want 0", o.Live.FTSBackfillRemaining)
	}
	if o.Live.OutboxPending == nil || *o.Live.OutboxPending != 0 {
		t.Errorf("outbox_pending = %v, want 0", o.Live.OutboxPending)
	}
	if len(o.Live.BreakersOpen) != 1 || o.Live.BreakersOpen[0] != "short_context" {
		t.Errorf("breakers_open = %v, want [short_context]", o.Live.BreakersOpen)
	}
	if o.Bots[0].LastSeenAt == nil || *o.Bots[0].LastSeenAt != "2026-11-12T23:59:30Z" || o.Bots[1].LastSeenAt != nil {
		t.Errorf("bots = %+v, want qq-main last seen 2026-11-12T23:59:30Z and tg-main never", o.Bots)
	}
	want := map[string]int64{"calls": 3, "errors": 1, "prompt_tokens": 1250, "completion_tokens": 330, "reasoning_tokens": 120}
	for k, v := range want {
		if o.Today[k] != v {
			t.Errorf("today.%s = %d, want %d", k, o.Today[k], v)
		}
	}
}

func TestOverview_NoGatherer(t *testing.T) {
	h := newOverviewHarness(t, func(d *Deps) { d.Gatherer, d.Presence, d.Location = nil, nil, nil })
	var o overviewBody
	h.getJSON("/panel/api/overview", &o)
	if o.Live.QueueDepth != nil || o.Live.EmbeddingBacklog != nil || o.Live.BreakersOpen == nil || len(o.Live.BreakersOpen) != 0 {
		t.Errorf("live = %+v, want nulls and no open breakers", o.Live)
	}
	if o.Timezone != "UTC" || len(o.Bots) != 2 || o.Bots[0].LastSeenAt != nil {
		t.Errorf("timezone %q, bots %+v", o.Timezone, o.Bots)
	}
}

func TestOverview_RequiresSession(t *testing.T) {
	h := newOverviewHarness(t, nil)
	for _, path := range []string{"/panel/api/overview", "/panel/api/usage", "/panel/api/usage/calls", "/panel/api/metrics/live", "/panel/api/config"} {
		if w := h.do(http.MethodGet, path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: %d, want 401", path, w.Code)
		}
	}
}
