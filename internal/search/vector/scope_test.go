package vector

import (
	"context"
	"slices"
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
