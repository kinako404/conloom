package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/embedding"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const testBot = "bot_test"

var (
	groupKey   = store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "10001")
	group2Key  = store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "10002")
	privateKey = store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "20001")
	shanghai   = mustLocation("Asia/Shanghai")
)

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// at is 09:00 plus minutes on 2026-11-13 in Asia/Shanghai.
func at(minutes int) time.Time {
	return time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

// vocab are the words the fake embedding service tells apart: a text's
// vector counts each of them, plus a small constant.
var vocab = []string{"草莓", "猫", "旅行", "工作", "火锅"}

// fakeEmbedder is an OpenAI-compatible embeddings service. Model "m2" gives
// vectors one dimension longer than other models. It does not record the
// indexer's dimension probes.
type fakeEmbedder struct {
	srv *httptest.Server

	mu      sync.Mutex
	inputs  [][]string
	fail    bool
	invalid bool
	// extraDims lengthens every vector, as another service would.
	extraDims int
	delay     time.Duration
	onInput   func()
	requests  int
}

func newFakeEmbedder(t *testing.T) *fakeEmbedder {
	t.Helper()
	f := &fakeEmbedder{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.requests++
		fail, invalid, delay, hook := f.fail, f.invalid, f.delay, f.onInput
		if !fail && !slices.Equal(req.Input, []string{dimsProbe}) {
			f.inputs = append(f.inputs, req.Input)
		}
		extra := f.extraDims
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if fail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if hook != nil {
			hook()
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		var data []item
		for i, text := range req.Input {
			v := fakeVector(req.Model, text)
			for range extra {
				v = append(v, 0.1)
			}
			if invalid {
				v[0] = 1e39
			}
			data = append(data, item{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func fakeVector(model, text string) []float64 {
	v := make([]float64, 0, len(vocab)+2)
	for _, w := range vocab {
		v = append(v, float64(strings.Count(text, w)))
	}
	v = append(v, 0.1)
	if model == "m2" {
		v = append(v, 0.1)
	}
	return v
}

func (f *fakeEmbedder) client(model string, batch int) *embedding.Client {
	return embedding.New(config.EmbeddingConfig{Endpoint: f.srv.URL + "/v1", Model: model,
		Timeout: conloomapi.Duration{Duration: 5 * time.Second}, BatchSize: batch}, nil)
}

func (f *fakeEmbedder) set(fn func(f *fakeEmbedder)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// texts returns every text embedded so far, and forgets them.
func (f *fakeEmbedder) take() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := f.inputs
	f.inputs = nil
	return in
}

func (f *fakeEmbedder) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

type env struct {
	t        *testing.T
	st       *sqlite.DB
	dbPath   string
	policies *policy.Service
	emb      *fakeEmbedder
	// override changes the stored policies as the indexer sees them, for
	// combinations that policy validation rejects.
	override map[string]func(p *conloomapi.GroupPolicy)
	// cfg decides which private chats are allowed; nil allows all.
	cfg     *config.Config
	reg     *prometheus.Registry
	logs    *lockedBuffer
	metrics *obs.Metrics
	now     time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "conloom.db")
	st, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := conloomapi.DefaultGroupPolicy()
	p.Memory.Enabled, p.Concurrency.AllowLongRequests = true, true
	reg := prometheus.NewRegistry()
	return &env{t: t, st: st, dbPath: dbPath, policies: policy.NewService(st, p), emb: newFakeEmbedder(t),
		override: map[string]func(p *conloomapi.GroupPolicy){}, reg: reg, logs: &lockedBuffer{}, metrics: obs.NewMetrics(reg), now: at(24 * 60)}
}

func (e *env) indexer(model string, batch int) *Indexer {
	ix := NewIndexer(IndexerDeps{Store: e.st, Embedder: e.emb.client(model, batch), Policies: overridden{e},
		Reviewer: review.New(nil, review.Options{}), Location: shanghai, Metrics: e.metrics, Config: e.cfg,
		Logger: slog.New(slog.NewTextHandler(e.logs, nil))})
	ix.now = func() time.Time { return e.now }
	return ix
}

func (e *env) searcher(model string) *Searcher {
	return NewSearcher(SearcherDeps{Store: e.st, Embedder: e.emb.client(model, 16), Metrics: e.metrics})
}

func (e *env) run(ix *Indexer) {
	e.t.Helper()
	if err := ix.RunOnce(context.Background()); err != nil {
		e.t.Fatalf("RunOnce: %v", err)
	}
}

func (e *env) setPolicy(convKey string, fn func(p *conloomapi.GroupPolicy)) {
	e.t.Helper()
	pv, err := e.policies.Current(context.Background(), convKey)
	if err != nil {
		e.t.Fatal(err)
	}
	p := pv.Policy
	fn(&p)
	if _, err := e.policies.Update(context.Background(), convKey, conloomapi.PolicyPut{Policy: p, Reason: "test"}, "admin"); err != nil {
		e.t.Fatal(err)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type overridden struct{ e *env }

func (o overridden) Current(ctx context.Context, convKey string) (store.PolicyVersion, error) {
	pv, err := o.e.policies.Current(ctx, convKey)
	if fn := o.e.override[convKey]; fn != nil {
		fn(&pv.Policy)
	}
	return pv, err
}

var msgCount int

// say stores a message of user ("" for the bot) named name in convKey and
// returns its seq.
func (e *env) say(convKey, user, name, text string, when time.Time) int64 {
	e.t.Helper()
	msgCount++
	id := fmt.Sprintf("msg_%05d", msgCount)
	parts := strings.Split(convKey, ":")
	m := store.MessageRecord{ID: id, BotInstanceID: testBot, ConversationKey: convKey, Platform: "qq",
		PlatformMessageID: "p" + id, ActorID: "user_" + user, PlatformUserID: user, DisplayName: name, Text: text, OccurredAt: when}
	if user == "" {
		m.ActorID, m.PlatformUserID, m.DisplayName, m.FromBot = "bot_30001", "30001", "织语", true
	}
	ev := store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "k_" + id, RequestHash: "h", EventID: "evt_" + id,
		Response: []byte("{}"), RawEvent: []byte("{}")}
	conv := store.ConversationRecord{BotInstanceID: testBot, ID: parts[3], Type: parts[2], Platform: "qq", PlatformConversationID: parts[3]}
	if _, err := e.st.Ingest(context.Background(), store.Ingest{Event: ev, Conversation: &conv, Message: &m}); err != nil {
		e.t.Fatalf("Ingest: %v", err)
	}
	msgs, err := e.st.RecentMessages(context.Background(), convKey, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	return msgs[0].Seq
}

func (e *env) chunks(convKey, model string) []store.MemoryChunk {
	e.t.Helper()
	cs, err := e.st.LoadChunks(context.Background(), convKey, model, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return cs
}

func (e *env) cursor(convKey string) int64 {
	e.t.Helper()
	st, err := e.st.ChunkState(context.Background(), convKey)
	if err != nil {
		e.t.Fatal(err)
	}
	return st.CursorSeq
}

// metric returns the value of the counter, gauge or histogram sample count
// name with the given label value ("" for none), 0 when absent.
func (e *env) metric(name, label string) float64 {
	e.t.Helper()
	mfs, err := e.reg.Gather()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if label != "" && (len(m.GetLabel()) == 0 || m.GetLabel()[0].GetValue() != label) {
				continue
			}
			switch {
			case m.Counter != nil:
				return m.GetCounter().GetValue()
			case m.Gauge != nil:
				return m.GetGauge().GetValue()
			case m.Histogram != nil:
				return float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}

func (e *env) requests(result string) float64 {
	return e.metric("conloom_embedding_requests_total", result)
}

// flat joins every embedded text.
func flat(inputs [][]string) []string {
	var out []string
	for _, in := range inputs {
		out = append(out, in...)
	}
	return out
}

func assertNoIDs(t *testing.T, texts []string) {
	t.Helper()
	for _, text := range texts {
		for _, id := range []string{"10001", "20001", "20002", "30001", "user_", "bot_", "msg_"} {
			if strings.Contains(text, id) {
				t.Errorf("embedded text shows %q:\n%s", id, text)
			}
		}
	}
}
