// Package search defines what the recall tool needs from long-term retrieval:
// a time range and semantic search over a conversation's past chunks.
package search

import (
	"context"
	"time"
)

// Range limits a search to messages that occurred within it. A zero After or
// Before leaves that side unbounded.
type Range struct {
	After, Before time.Time
}

// Hit is a stretch of one conversation, from FromSeq to ToSeq (message seqs,
// inclusive), found for a query. A higher Score is more relevant; scores are
// only comparable within one search.
type Hit struct {
	FromSeq, ToSeq int64
	Score          float64
}

// VectorSearcher finds the stretches of a conversation that are semantically
// closest to a query. It returns no hits, not an error, when semantic search is
// unavailable, so that callers fall back to keywords alone.
type VectorSearcher interface {
	Search(ctx context.Context, convKey, query string, r Range, limit int) ([]Hit, error)
}
