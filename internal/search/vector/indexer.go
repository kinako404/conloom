package vector

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/embedding"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/skills/tools"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// roundInterval is how often the indexer runs; roundBudget bounds the
	// chunks it embeds in one round.
	roundInterval = time.Minute
	roundBudget   = 200
	// maxBackoff bounds the pause after failed rounds, which starts at
	// roundInterval and doubles.
	maxBackoff = 10 * time.Minute
	// forwardScan bounds the new messages read per conversation and round;
	// rebuildScan the messages read for one rebuild.
	forwardScan = 1000
	rebuildScan = 5000
	// dimsProbe is embedded to learn the service's vector length when no
	// vector has been seen yet but chunks are stored.
	dimsProbe = "维度"
)

// Embedder turns texts into vectors; *embedding.Client is one, and
// *usage.Embedder one that records its calls. The Indexer's calls carry the
// conversation they embed as their usage.Attribution.
type Embedder interface {
	Model() string
	BatchSize() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Store is what the Indexer reads and writes; *sqlite.DB is one.
type Store interface {
	ListConversations(ctx context.Context, convType string) ([]store.ConversationRecord, error)
	MessagesAfter(ctx context.Context, convKey string, afterSeq int64, limit int) ([]store.MessageRecord, error)
	MessagesUpTo(ctx context.Context, convKey string, seq int64, limit int) ([]store.MessageRecord, error)
	CountMessagesAfter(ctx context.Context, convKey string, afterSeq int64) (int, error)
	ListMemberMemories(ctx context.Context, convKey string) ([]store.MemberMemory, error)
	ChunkState(ctx context.Context, convKey string) (store.ChunkState, error)
	ForgetStamp(ctx context.Context, convKey string) (string, error)
	WriteChunks(ctx context.Context, w store.ChunkWrite) (bool, error)
	SwitchChunkModel(ctx context.Context, convKey, model string) error
	ChunkRebuilds(ctx context.Context, convKey string, limit int) ([]store.ChunkRebuild, error)
	CountChunkRebuilds(ctx context.Context) (int, error)
	DeleteStaleChunks(ctx context.Context, convKey, model string) (int, error)
	ReconcileForgottenChunks(ctx context.Context) (int, error)
	ChunkDims(ctx context.Context, convKey, model string) ([]int, error)
	RequeueChunkDims(ctx context.Context, convKey, model string, dims int) (int, error)
}

// IndexerDeps are what the Indexer needs. Reviewer and IsCommand are those of
// recall (tools.RecallDeps), so that chunks hold only what recall may show;
// Location renders the chunks' times. Config decides, like for memory
// consolidation, which private chats are embedded (config.BotConfig.PrivateAllowed);
// without it all are. Metrics and Logger may be nil.
type IndexerDeps struct {
	// Interval is how often Start runs a round, roundInterval when 0.
	Interval  time.Duration
	Store     Store
	Embedder  Embedder
	Policies  tools.PolicySource
	Reviewer  tools.OutputReviewer
	IsCommand func(text, prefix string) bool
	Location  *time.Location
	Config    *config.Config
	Metrics   *obs.Metrics
	Logger    *slog.Logger
}

// Indexer embeds the chunks of every conversation where memory is in effect
// (and, for a private chat, that the bot still allows),
// in rounds a minute apart of at most 200 chunks each: first the chunks that
// forgetting or a change of model queued to be rebuilt, newest first, then
// the conversation's new closed chunks. A failed round pauses the indexer,
// for a minute after the first failure and twice as long after each further
// one, up to ten minutes.
type Indexer struct {
	d   IndexerDeps
	now func() time.Time

	mu      sync.Mutex
	backoff time.Duration
	next    time.Time
	cancel  context.CancelFunc
	done    chan struct{}
	// reconciled is set once the first round has applied forgetting that
	// the chunks missed; only rounds touch it.
	reconciled bool
	// dims is the length of the service's vectors, 0 until one is seen;
	// only rounds touch it.
	dims int
}

func NewIndexer(d IndexerDeps) *Indexer {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	return &Indexer{d: d, now: time.Now, done: make(chan struct{})}
}

// Start runs the indexer's rounds in the background until Close.
func (ix *Indexer) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	ix.mu.Lock()
	ix.cancel = cancel
	ix.mu.Unlock()
	go func() {
		defer close(ix.done)
		t := time.NewTicker(cmp.Or(ix.d.Interval, roundInterval))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ix.tick(ctx)
			}
		}
	}()
}

// Close stops the rounds started by Start and waits for the current one.
func (ix *Indexer) Close() {
	ix.mu.Lock()
	cancel := ix.cancel
	ix.cancel = nil
	ix.mu.Unlock()
	if cancel != nil {
		cancel()
		<-ix.done
	}
}

// tick runs a round unless the indexer is pausing after failures.
func (ix *Indexer) tick(ctx context.Context) {
	now := ix.now()
	ix.mu.Lock()
	wait := now.Before(ix.next)
	ix.mu.Unlock()
	if wait {
		return
	}
	err := ix.RunOnce(ctx)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err == nil {
		ix.backoff, ix.next = 0, time.Time{}
		return
	}
	ix.backoff = min(max(2*ix.backoff, roundInterval), maxBackoff)
	ix.next = now.Add(ix.backoff)
	if ctx.Err() == nil {
		ix.d.Logger.Warn("embedding round failed", "retry_in", ix.backoff, "error", err)
	}
}

// RunOnce runs one round. It stops at the first error, leaving what is left
// for the next round. The indexer's first round starts by applying to the
// chunks any forgetting they missed (store ReconcileForgottenChunks), e.g.
// done by an older binary.
func (ix *Indexer) RunOnce(ctx context.Context) error {
	if !ix.reconciled {
		n, err := ix.d.Store.ReconcileForgottenChunks(ctx)
		if err != nil {
			return err
		}
		ix.reconciled = true
		if n > 0 {
			ix.d.Logger.Info("semantic search caught up with forgetting", "members", n)
		}
	}
	model := ix.d.Embedder.Model()
	budget := roundBudget
	pending := 0
	defer func() {
		if ix.d.Metrics == nil {
			return
		}
		if queued, err := ix.d.Store.CountChunkRebuilds(ctx); err == nil {
			ix.d.Metrics.EmbeddingBacklog.Set(float64(queued + pending))
		}
	}()
	for _, convType := range []string{conloomapi.ConvGroup, conloomapi.ConvPrivate} {
		convs, err := ix.d.Store.ListConversations(ctx, convType)
		if err != nil {
			return err
		}
		for _, c := range convs {
			pv, on, err := tools.MemoryOn(ctx, ix.d.Policies, c.Key)
			if err != nil {
				return err
			}
			if !on || !ix.privateAllowed(c) {
				continue
			}
			if budget > 0 {
				cctx := usage.WithAttribution(ctx, usage.Attribution{BotInstanceID: c.BotInstanceID, ConversationKey: c.Key})
				used, err := ix.conversation(cctx, c.Key, c.Type, model, pv.Policy, budget)
				if err != nil {
					return err
				}
				budget -= used
			}
			left, err := ix.unchunked(ctx, c.Key)
			if err != nil {
				return err
			}
			pending += left
		}
	}
	return nil
}

// privateAllowed reports whether c is not a private chat, or one whose user
// the bot's private_chat setting still allows.
func (ix *Indexer) privateAllowed(c store.ConversationRecord) bool {
	if ix.d.Config == nil || c.Type != conloomapi.ConvPrivate {
		return true
	}
	bot, ok := ix.d.Config.Bot(c.BotInstanceID)
	return ok && bot.PrivateAllowed(c.Platform, c.PlatformConversationID)
}

// unchunked estimates the chunks of convKey still to be cut: its messages
// after the cursor, chunkMaxLines to a chunk.
func (ix *Indexer) unchunked(ctx context.Context, convKey string) (int, error) {
	st, err := ix.d.Store.ChunkState(ctx, convKey)
	if err != nil {
		return 0, err
	}
	n, err := ix.d.Store.CountMessagesAfter(ctx, convKey, st.CursorSeq)
	return (n + chunkMaxLines - 1) / chunkMaxLines, err
}

// conversation embeds up to about budget chunks of convKey and returns how
// many it embedded.
func (ix *Indexer) conversation(ctx context.Context, convKey, convType, model string, pol conloomapi.GroupPolicy, budget int) (int, error) {
	st, err := ix.d.Store.ChunkState(ctx, convKey)
	if err != nil {
		return 0, err
	}
	if st.Model != model {
		if err := ix.d.Store.SwitchChunkModel(ctx, convKey, model); err != nil {
			return 0, err
		}
	}
	if err := ix.checkDims(ctx, convKey, model); err != nil {
		return 0, err
	}
	stamp, err := ix.d.Store.ForgetStamp(ctx, convKey)
	if err != nil {
		return 0, err
	}
	members, err := ix.d.Store.ListMemberMemories(ctx, convKey)
	if err != nil {
		return 0, err
	}
	earlier := func(ctx context.Context, beforeSeq int64, n int) ([]store.MessageRecord, error) {
		return ix.d.Store.MessagesUpTo(ctx, convKey, beforeSeq-1, n)
	}
	transcript := func(msgs []store.MessageRecord) ([]tools.TranscriptLine, error) {
		return tools.Transcript(ctx, msgs, earlier, members, convType, pol, ix.d.Reviewer, ix.d.IsCommand, ix.d.Location)
	}

	used, done, err := ix.rebuild(ctx, convKey, model, stamp, budget, transcript)
	if err != nil || !done {
		return used, err
	}
	if _, err := ix.d.Store.DeleteStaleChunks(ctx, convKey, model); err != nil {
		return used, err
	}
	if used >= budget {
		return used, nil
	}
	n, err := ix.forward(ctx, convKey, model, stamp, st.CursorSeq, budget-used, transcript)
	return used + n, err
}

// checkDims queues the conversation's chunks of model whose vectors are not
// as long as the service's now (it was replaced without changing the model
// name) to be rebuilt.
func (ix *Indexer) checkDims(ctx context.Context, convKey, model string) error {
	stored, err := ix.d.Store.ChunkDims(ctx, convKey, model)
	if err != nil || len(stored) == 0 {
		return err
	}
	if ix.dims == 0 {
		vecs, err := ix.d.Embedder.Embed(ctx, []string{dimsProbe})
		ix.count(err)
		if err != nil {
			return err
		}
		ix.dims = len(vecs[0])
	}
	if len(stored) == 1 && stored[0] == ix.dims {
		return nil
	}
	n, err := ix.d.Store.RequeueChunkDims(ctx, convKey, model, ix.dims)
	if err == nil && n > 0 {
		ix.d.Logger.Warn("embedding dimensions differ from the stored chunks; rebuilding them",
			"conversation", convKey, "model", model, "stored_dims", stored, "dims", ix.dims, "chunks", n)
	}
	return err
}

// transcriptFunc renders a stretch of a conversation's messages as
// tools.Transcript does, looking back before it as needed.
type transcriptFunc func(msgs []store.MessageRecord) ([]tools.TranscriptLine, error)

// rebuild re-cuts and embeds the conversation's queued stretches, newest
// first, until about budget chunks are embedded. done reports that the queue
// was emptied and nothing stands in the way of going on.
func (ix *Indexer) rebuild(ctx context.Context, convKey, model, stamp string, budget int, transcript transcriptFunc) (used int, done bool, err error) {
	queue, err := ix.d.Store.ChunkRebuilds(ctx, convKey, budget+1)
	if err != nil {
		return 0, false, err
	}
	if len(queue) == 0 {
		return 0, true, nil
	}
	var rebuilt []store.ChunkRebuild
	var chunks []*chunk
	for _, r := range queue {
		if len(chunks) >= budget {
			break
		}
		msgs, err := ix.d.Store.MessagesAfter(ctx, convKey, r.StartSeq-1, int(min(r.EndSeq-r.StartSeq+1, rebuildScan)))
		if err != nil {
			return 0, false, err
		}
		msgs = slices.DeleteFunc(msgs, func(m store.MessageRecord) bool { return m.Seq > r.EndSeq })
		lines, err := transcript(msgs)
		if err != nil {
			return 0, false, err
		}
		rebuilt = append(rebuilt, r)
		chunks = append(chunks, split(lines)...)
	}
	records, err := ix.embed(ctx, convKey, model, chunks)
	if err != nil {
		return 0, false, err
	}
	ok, err := ix.d.Store.WriteChunks(ctx, store.ChunkWrite{ConversationKey: convKey, Model: model, ForgetStamp: stamp,
		Chunks: records, Rebuilt: rebuilt})
	if err != nil || !ok {
		return len(records), false, err
	}
	return len(records), len(rebuilt) == len(queue), nil
}

// forward cuts the conversation's messages after cursor into chunks and
// embeds up to budget closed ones, moving the cursor past them.
func (ix *Indexer) forward(ctx context.Context, convKey, model, stamp string, cursor int64, budget int, transcript transcriptFunc) (int, error) {
	msgs, err := ix.d.Store.MessagesAfter(ctx, convKey, cursor, forwardScan)
	if err != nil || len(msgs) == 0 {
		return 0, err
	}
	truncated := len(msgs) == forwardScan
	newest := msgs[len(msgs)-1].Seq
	lines, err := transcript(msgs)
	if err != nil {
		return 0, err
	}
	chunks := split(lines)
	closed := len(chunks)
	if closed > 0 && (truncated || !chunks[closed-1].closedAt(ix.now())) {
		closed--
	}
	next := newest
	switch {
	case closed > budget:
		closed = budget
		next = chunks[closed-1].lines[len(chunks[closed-1].lines)-1].Seq
	case closed < len(chunks):
		next = chunks[closed].lines[0].Seq - 1
	}
	records, err := ix.embed(ctx, convKey, model, chunks[:closed])
	if err != nil {
		return 0, err
	}
	if next <= cursor && len(records) == 0 {
		return 0, nil
	}
	_, err = ix.d.Store.WriteChunks(ctx, store.ChunkWrite{ConversationKey: convKey, Model: model, ForgetStamp: stamp,
		Chunks: records, CursorSeq: next})
	return len(records), err
}

// embed embeds chunks in batches of the embedder's batch size.
func (ix *Indexer) embed(ctx context.Context, convKey, model string, chunks []*chunk) ([]store.MemoryChunk, error) {
	out := make([]store.MemoryChunk, 0, len(chunks))
	size := ix.d.Embedder.BatchSize()
	for batch := range slices.Chunk(chunks, size) {
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.text()
		}
		vecs, err := ix.d.Embedder.Embed(ctx, texts)
		ix.count(err)
		if err != nil {
			return nil, err
		}
		if d := len(vecs[0]); d != ix.dims {
			if ix.dims != 0 {
				ix.d.Logger.Warn("embedding dimensions changed", "model", model, "was", ix.dims, "now", d)
			}
			ix.dims = d
		}
		for i, c := range batch {
			out = append(out, c.record(convKey, model, vecs[i]))
		}
	}
	return out, nil
}

func (ix *Indexer) count(err error) {
	if ix.d.Metrics != nil {
		ix.d.Metrics.EmbeddingRequests.WithLabelValues(embedding.Result(err)).Inc()
	}
}
