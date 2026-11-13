package vector

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
)

type callSink struct {
	mu    sync.Mutex
	calls []store.ModelCall
}

func (s *callSink) InsertModelCalls(_ context.Context, calls []store.ModelCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, calls...)
	return nil
}

func TestIndexer_AttributesEmbeddingsToConversations(t *testing.T) {
	e := newEnv(t)
	for i := range 8 {
		e.say(groupKey, "20001", "小明", fmt.Sprintf("第%d句", i), at(i))
		e.say(group2Key, "20002", "小红", fmt.Sprintf("另一句%d", i), at(i))
	}
	e.now = at(60)
	sink := &callSink{}
	rec := usage.NewRecorder(sink, usage.RecorderOptions{Flush: time.Hour})
	ix := e.indexer("m1", 16)
	ix.d.Embedder = usage.NewEmbedder(e.emb.client("m1", 16), rec, store.PurposeEmbedIndex)
	e.run(ix)
	if err := rec.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := map[string]int{}
	for _, c := range sink.calls {
		if c.BotInstanceID != testBot || c.TaskID != "" || c.Purpose != store.PurposeEmbedIndex || c.Role != usage.RoleEmbedding ||
			c.Model != "m1" || c.Outcome != store.OutcomeOK {
			t.Errorf("call = %+v", c)
		}
		got[c.ConversationKey]++
	}
	if len(sink.calls) != 2 || got[groupKey] != 1 || got[group2Key] != 1 {
		t.Errorf("calls by conversation = %v, want one for each group", got)
	}
}
