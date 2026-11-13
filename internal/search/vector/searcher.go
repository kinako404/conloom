package vector

import (
	"cmp"
	"container/list"
	"context"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/embedding"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
)

const (
	// DefaultQueryTimeout bounds embedding a query.
	DefaultQueryTimeout = 2 * time.Second
	// cachedConversations is how many conversations' vectors stay in memory.
	cachedConversations = 8
	// After breakerFailures queries in a row fail to be embedded, searches
	// skip the service for breakerCooldown; then one query tries it again.
	breakerFailures = 3
	breakerCooldown = 60 * time.Second
	// DefaultMinScore is the cosine similarity below which a chunk counts as
	// unrelated, the default of embedding.min_score. Calibrated on bge-m3:
	// related chunks scored about 0.50–0.63, most unrelated pairs below 0.46.
	// Another model needs calibrating again.
	DefaultMinScore = 0.48
	// DefaultMargin is how far below the best chunk of a search others may
	// score and still be hits.
	DefaultMargin = 0.05
	// loggedScores is how many of the best scores a search logs at debug
	// level.
	loggedScores = 5
)

// ChunkReader is what the Searcher reads; *sqlite.DB is one.
type ChunkReader interface {
	ChunkStamp(ctx context.Context, convKey, model string, upToID int64) (store.ChunkStamp, error)
	LoadChunks(ctx context.Context, convKey, model string, afterID int64) ([]store.MemoryChunk, error)
}

// SearcherDeps are what the Searcher needs. QueryTimeout defaults to
// DefaultQueryTimeout. Chunks less similar to the query than MinScore, or
// than the best chunk's score minus Margin (when Margin > 0), are never
// returned; with both 0 the closest ones are, however distant. Metrics and
// Logger may be nil.
type SearcherDeps struct {
	Store        ChunkReader
	Embedder     Embedder
	QueryTimeout time.Duration
	MinScore     float64
	Margin       float64
	Metrics      *obs.Metrics
	Logger       *slog.Logger
}

// Searcher is a search.VectorSearcher over the chunks of the embedder's
// model. It keeps the vectors of the last 8 conversations searched in
// memory, brought up to date with the stored chunks at every search, so new
// chunks are found and deleted ones never are. After 3 queries in a row fail
// to be embedded it returns no hits without asking the service for a minute,
// then lets one query through to try it.
type Searcher struct {
	d   SearcherDeps
	now func() time.Time

	mu    sync.Mutex
	cache map[string]*list.Element
	lru   *list.List

	bmu       sync.Mutex
	failures  int
	openUntil time.Time
	trying    bool
}

var _ search.VectorSearcher = (*Searcher)(nil)

func NewSearcher(d SearcherDeps) *Searcher {
	if d.QueryTimeout <= 0 {
		d.QueryTimeout = DefaultQueryTimeout
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Searcher{d: d, now: time.Now, cache: map[string]*list.Element{}, lru: list.New()}
}

// cached is a chunk ready for scoring.
type cached struct {
	from, to       int64
	startAt, endAt time.Time
	vec            []float32
	norm           float64
}

// entry is a conversation's cached chunks, those with an ID up to maxID. It
// is never changed once cached.
type entry struct {
	convKey, model string
	maxID          int64
	chunks         []cached
}

// Search returns the up to limit chunks of convKey that overlap r (a chunk
// ending at After counts, one starting at Before does not) and are
// closest to query by cosine similarity, at least MinScore and within Margin
// of the best, best first. It logs the best scores at debug level, never the
// query or any text. When the query cannot be embedded in time it logs why
// and returns no hits, without an error; so it does while the breaker is
// open.
func (s *Searcher) Search(ctx context.Context, convKey, query string, r search.Range, limit int) ([]search.Hit, error) {
	if s.d.Metrics != nil {
		defer func(start time.Time) {
			s.d.Metrics.SearchSeconds.WithLabelValues("vector").Observe(time.Since(start).Seconds())
		}(time.Now())
	}
	if limit <= 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	model := s.d.Embedder.Model()
	trial, ok := s.allow()
	if !ok {
		return nil, nil
	}
	qctx, cancel := context.WithTimeout(ctx, s.d.QueryTimeout)
	vecs, err := s.d.Embedder.Embed(qctx, []string{query})
	cancel()
	s.record(ctx, err, trial)
	if s.d.Metrics != nil {
		s.d.Metrics.EmbeddingRequests.WithLabelValues(embedding.Result(err)).Inc()
	}
	if err != nil {
		s.d.Logger.Warn("embedding the query failed; no semantic hits", "conversation", convKey, "result", embedding.Result(err), "error", err)
		return nil, nil
	}
	q := vecs[0]
	qnorm := norm(q)
	if qnorm == 0 {
		return nil, nil
	}
	e, err := s.load(ctx, convKey, model)
	if err != nil {
		return nil, err
	}
	var hits []search.Hit
	for _, c := range e.chunks {
		if len(c.vec) != len(q) || c.norm == 0 ||
			!r.After.IsZero() && c.endAt.Before(r.After) || !r.Before.IsZero() && !c.startAt.Before(r.Before) {
			continue
		}
		hits = append(hits, search.Hit{FromSeq: c.from, ToSeq: c.to, Score: dot(q, c.vec) / (qnorm * c.norm)})
	}
	slices.SortFunc(hits, func(a, b search.Hit) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(b.ToSeq, a.ToSeq))
	})
	top := make([]string, 0, loggedScores)
	for _, h := range hits[:min(len(hits), loggedScores)] {
		top = append(top, strconv.FormatFloat(h.Score, 'f', 3, 64))
	}
	floor := s.d.MinScore
	if len(hits) > 0 && s.d.Margin > 0 {
		floor = max(floor, hits[0].Score-s.d.Margin)
	}
	hits = slices.DeleteFunc(hits, func(h search.Hit) bool { return h.Score < floor })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	s.d.Logger.Debug("semantic search scores", "top_scores", "["+strings.Join(top, " ")+"]", "hits", len(hits))
	return hits, nil
}

// allow reports whether a query may be sent to the service: always while the
// breaker is closed; once open, only as the single trial after the cooldown.
func (s *Searcher) allow() (trial, ok bool) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	if s.failures < breakerFailures {
		return false, true
	}
	if s.trying || s.now().Before(s.openUntil) {
		return false, false
	}
	s.trying = true
	return true, true
}

// record counts the outcome of a query sent to the service. Queries the
// caller gave up on do not count.
func (s *Searcher) record(ctx context.Context, err error, trial bool) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	if trial {
		s.trying = false
	}
	switch {
	case err == nil:
		s.failures = 0
	case ctx.Err() == nil:
		s.failures++
		if s.failures >= breakerFailures {
			s.openUntil = s.now().Add(breakerCooldown)
		}
	}
}

// load returns the conversation's chunks of model as currently stored: the
// cached ones when none was deleted since, plus any added; otherwise all of
// them read again.
func (s *Searcher) load(ctx context.Context, convKey, model string) (*entry, error) {
	s.mu.Lock()
	var old *entry
	if el, ok := s.cache[convKey]; ok {
		old = el.Value.(*entry)
		if old.model != model {
			old = nil
		}
	}
	s.mu.Unlock()

	var upTo int64
	if old != nil {
		upTo = old.maxID
	}
	stamp, err := s.d.Store.ChunkStamp(ctx, convKey, model, upTo)
	if err != nil {
		return nil, err
	}
	e := old
	switch {
	case old != nil && stamp.UpTo == len(old.chunks) && stamp.MaxID == old.maxID:
	case old != nil && stamp.UpTo == len(old.chunks):
		added, err := s.d.Store.LoadChunks(ctx, convKey, model, old.maxID)
		if err != nil {
			return nil, err
		}
		e = &entry{convKey: convKey, model: model, maxID: old.maxID, chunks: slices.Clip(old.chunks)}
		e.add(added)
	default:
		all, err := s.d.Store.LoadChunks(ctx, convKey, model, 0)
		if err != nil {
			return nil, err
		}
		e = &entry{convKey: convKey, model: model}
		e.add(all)
	}
	s.put(e)
	return e, nil
}

func (e *entry) add(chunks []store.MemoryChunk) {
	for _, c := range chunks {
		e.chunks = append(e.chunks, cached{from: c.StartSeq, to: c.EndSeq, startAt: c.StartAt, endAt: c.EndAt, vec: c.Vector, norm: norm(c.Vector)})
		e.maxID = max(e.maxID, c.ID)
	}
}

// put caches e as its conversation's newest entry, evicting the least
// recently searched conversation beyond cachedConversations.
func (s *Searcher) put(e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.cache[e.convKey]; ok {
		el.Value = e
		s.lru.MoveToFront(el)
		return
	}
	s.cache[e.convKey] = s.lru.PushFront(e)
	for s.lru.Len() > cachedConversations {
		last := s.lru.Back()
		s.lru.Remove(last)
		delete(s.cache, last.Value.(*entry).convKey)
	}
}

// cachedKeys returns the cached conversations, most recently searched first.
func (s *Searcher) cachedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for el := s.lru.Front(); el != nil; el = el.Next() {
		keys = append(keys, el.Value.(*entry).convKey)
	}
	return keys
}

func dot(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

func norm(v []float32) float64 { return math.Sqrt(dot(v, v)) }
