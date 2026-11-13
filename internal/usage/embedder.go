package usage

import (
	"context"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// RoleEmbedding is the Role of embedding calls.
const RoleEmbedding = "embedding"

// EmbedClient is an embedding service client that reports token usage;
// *embedding.Client is one.
type EmbedClient interface {
	Model() string
	BatchSize() int
	EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error) // int = prompt tokens, 0 if unknown
}

// Embedder is a vector.Embedder that records each request it makes, with
// the attribution of its ctx and the purpose it was made for.
type Embedder struct {
	c       EmbedClient
	rec     *Recorder
	purpose string
}

// NewEmbedder wraps c, recording its calls to rec (nil records nothing) as
// purpose (store.PurposeEmbedIndex or PurposeEmbedQuery).
func NewEmbedder(c EmbedClient, rec *Recorder, purpose string) *Embedder {
	return &Embedder{c: c, rec: rec, purpose: purpose}
}

func (e *Embedder) Model() string { return e.c.Model() }

func (e *Embedder) BatchSize() int { return e.c.BatchSize() }

// Embed embeds texts with the client and records the call. No texts make
// no request and no record.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	start := time.Now()
	vecs, tokens, err := e.c.EmbedUsage(ctx, texts)
	outcome := Outcome(err)
	if err != nil && ctx.Err() != nil {
		outcome = Outcome(ctx.Err())
	}
	a := AttributionFrom(ctx)
	e.rec.Record(store.ModelCall{
		At: start.UTC(), BotInstanceID: a.BotInstanceID, ConversationKey: a.ConversationKey, TaskID: a.TaskID,
		Purpose: e.purpose, Role: RoleEmbedding, Model: e.c.Model(), PromptTokens: tokens,
		LatencyMS: time.Since(start).Milliseconds(), Outcome: outcome,
	})
	return vecs, err
}
