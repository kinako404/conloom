package vector

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestSearch_PanelScopeAcrossChats(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	panelChat := func(id string) string {
		return store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, id)
	}
	a, b := panelChat("panel_a"), panelChat("panel_b")
	berryA := e.say(a, "panel_admin", "小明", "我喜欢草莓", at(0))
	catB := e.say(b, "panel_admin", "小明", "我养了一只猫", at(60))
	berryB := e.say(b, "panel_admin", "小明", "草莓蛋糕", at(120))
	berryP := e.say(privateKey, "20001", "小明", "草莓好吃", at(180))
	e.run(e.indexer("m1", 16))
	s := e.searcher("m1")
	type hit struct {
		conv string
		seq  int64
	}
	found := func(convKey, query string) []hit {
		t.Helper()
		hits, err := s.Search(ctx, convKey, query, search.Range{}, 10)
		if err != nil {
			t.Fatalf("Search from %s: %v", convKey, err)
		}
		var out []hit
		for _, h := range hits {
			if h.Score > 0.9 {
				out = append(out, hit{h.ConversationKey, h.FromSeq})
			}
		}
		return out
	}

	if got := found(a, "猫"); len(got) != 1 || got[0] != (hit{b, catB}) {
		t.Errorf("猫 from A = %+v, want B's chunk", got)
	}
	got := found(b, "草莓")
	if len(got) != 2 || !slices.Contains(got, hit{a, berryA}) || !slices.Contains(got, hit{b, berryB}) {
		t.Errorf("草莓 from B = %+v, want the chunks of A and B and not the platform chat's", got)
	}
	if got := found(privateKey, "草莓"); len(got) != 1 || got[0] != (hit{privateKey, berryP}) {
		t.Errorf("草莓 from the platform private chat = %+v, want only its own chunk", got)
	}

	if err := e.st.DeleteConversation(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := len(e.chunks(a, "m1")); n != 0 {
		t.Errorf("chunks of the deleted chat A = %d", n)
	}
	if got := found(b, "草莓"); len(got) != 1 || got[0] != (hit{b, berryB}) {
		t.Errorf("草莓 from B after deleting A = %+v, want only B's", got)
	}
}

// loadCounter is a ChunkReader that counts LoadChunks calls.
type loadCounter struct {
	ChunkReader
	mu    sync.Mutex
	loads int
}

func (c *loadCounter) LoadChunks(ctx context.Context, convKey, model string, afterID int64) ([]store.MemoryChunk, error) {
	c.mu.Lock()
	c.loads++
	c.mu.Unlock()
	return c.ChunkReader.LoadChunks(ctx, convKey, model, afterID)
}

func (c *loadCounter) take() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.loads
	c.loads = 0
	return n
}

func TestSearch_PanelScopeOfManyChatsStaysCached(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	const chats = cachedConversations + 2
	var keys []string
	for i := range chats {
		key := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, fmt.Sprintf("panel_%02d", i))
		keys = append(keys, key)
		e.say(key, "panel_admin", "小明", "我喜欢草莓", at(i))
	}
	e.run(e.indexer("m1", 16))
	reader := &loadCounter{ChunkReader: e.st}
	s := NewSearcher(SearcherDeps{Store: reader, Embedder: e.emb.client("m1", 16)})
	for i := range 3 {
		hits, err := s.Search(ctx, keys[0], "草莓", search.Range{}, chats)
		if err != nil || len(hits) != chats {
			t.Fatalf("search %d = %d hits, %v; want one per chat", i, len(hits), err)
		}
		loads := reader.take()
		if i == 0 && loads != chats {
			t.Fatalf("first search read chunks %d times, want once per chat (%d)", loads, chats)
		}
		if i > 0 && loads != 0 {
			t.Fatalf("search %d read chunks %d times, want none: the scope's %d chats must stay cached", i, loads, chats)
		}
	}
	if got := s.cachedKeys(); len(got) != chats {
		t.Errorf("cached = %d conversations, want all %d chats of the scope", len(got), chats)
	}
	if _, err := s.Search(ctx, groupKey, "草莓", search.Range{}, 1); err != nil {
		t.Fatal(err)
	}
	if got := s.cachedKeys(); len(got) != cachedConversations || got[0] != groupKey {
		t.Errorf("cached after another conversation's search = %v, want %d, newest first", got, cachedConversations)
	}
}
