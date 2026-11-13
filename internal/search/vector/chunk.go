// Package vector is the semantic search of long-term memory: an Indexer cuts
// conversations into chunks of a few messages and stores their embeddings,
// and a Searcher finds the chunks closest to a query.
package vector

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/skills/tools"
	"github.com/kinako404/conloom/internal/store"
)

const (
	// chunkGap is the longest pause within a chunk; a chunk whose last message
	// is older than that is closed.
	chunkGap = 30 * time.Minute
	// chunkMaxLines and chunkMaxRunes bound a chunk's lines and text.
	chunkMaxLines = 8
	chunkMaxRunes = 400
)

// chunk is a run of transcript lines embedded together.
type chunk struct {
	lines []tools.TranscriptLine
	runes int
}

func (c *chunk) text() string {
	texts := make([]string, len(c.lines))
	for i, l := range c.lines {
		texts[i] = l.Text
	}
	return strings.Join(texts, "\n")
}

// fits reports whether l may join c: within chunkGap of its last line and
// without going over chunkMaxLines or chunkMaxRunes.
func (c *chunk) fits(l tools.TranscriptLine) bool {
	if len(c.lines) == 0 {
		return true
	}
	last := c.lines[len(c.lines)-1]
	return len(c.lines) < chunkMaxLines && c.runes+1+utf8.RuneCountInString(l.Text) <= chunkMaxRunes &&
		l.At.Sub(last.At) <= chunkGap
}

// full reports whether no line can join c any more.
func (c *chunk) full() bool { return len(c.lines) >= chunkMaxLines }

// closedAt reports whether c is closed at now: full, or its last line is more
// than chunkGap old. A chunk followed by another is closed too.
func (c *chunk) closedAt(now time.Time) bool {
	return c.full() || now.Sub(c.lines[len(c.lines)-1].At) > chunkGap
}

func (c *chunk) record(convKey, model string, vec []float32) store.MemoryChunk {
	first, last := c.lines[0], c.lines[len(c.lines)-1]
	sum := sha256.Sum256([]byte(c.text()))
	return store.MemoryChunk{ConversationKey: convKey, StartSeq: first.Seq, EndSeq: last.Seq, StartAt: first.At, EndAt: last.At,
		TextHash: hex.EncodeToString(sum[:]), Model: model, Vector: vec}
}

// split cuts lines, in Seq order, into chunks.
func split(lines []tools.TranscriptLine) []*chunk {
	var out []*chunk
	for _, l := range lines {
		if len(out) == 0 || !out[len(out)-1].fits(l) {
			out = append(out, &chunk{})
		}
		c := out[len(out)-1]
		if len(c.lines) > 0 {
			c.runes++
		}
		c.lines = append(c.lines, l)
		c.runes += utf8.RuneCountInString(l.Text)
	}
	return out
}
