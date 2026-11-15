package sqlite

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/kinako404/conloom/internal/search"
)

func TestSearchMessagesIn(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	a, b := panelChatConv(testBot, "panel_a").Key, panelChatConv(testBot, "panel_b").Key
	inA := ftsMessage(a, "20001", "小明", "我喜欢草莓", ftsDay(0))
	inB := ftsMessage(b, "20001", "小明", "草莓草莓蛋糕", ftsDay(1))
	late := ftsMessage(b, "20001", "小明", "又买了草莓", ftsDay(3))
	other := ftsMessage(testConvKey, "20002", "小红", "草莓很甜", ftsDay(2))
	memInsertMessages(t, db, inA, inB, late, other)
	type hit struct {
		conv string
		seq  int64
	}
	search1 := func(keys []string, r search.Range, offset, limit int) []hit {
		t.Helper()
		hits, err := db.SearchMessagesIn(ctx, keys, []string{"草莓"}, r, offset, limit)
		if err != nil {
			t.Fatalf("SearchMessagesIn(%v): %v", keys, err)
		}
		var out []hit
		for i, h := range hits {
			if i > 0 && h.Score > hits[i-1].Score {
				t.Fatalf("hits not best first: %+v", hits)
			}
			out = append(out, hit{h.ConversationKey, h.Seq})
		}
		return out
	}

	all := search1([]string{a, b}, search.Range{}, 0, 10)
	want := []hit{{a, seqOf(t, db, inA.ID)}, {b, seqOf(t, db, inB.ID)}, {b, seqOf(t, db, late.ID)}}
	if len(all) != 3 {
		t.Fatalf("hits in A and B = %v, want their three messages", all)
	}
	for _, h := range want {
		if !slices.Contains(all, h) {
			t.Errorf("hits %v lack %v", all, h)
		}
	}
	if paged := append(search1([]string{a, b}, search.Range{}, 0, 2), search1([]string{a, b}, search.Range{}, 2, 2)...); !reflect.DeepEqual(paged, all) {
		t.Errorf("pages %v, want %v", paged, all)
	}
	if got := search1([]string{a, b}, search.Range{After: ftsDay(1), Before: ftsDay(3)}, 0, 10); !slices.Equal(got, want[1:2]) {
		t.Errorf("hits within days 1–2 = %v, want %v", got, want[1:2])
	}
	if got := search1(nil, search.Range{}, 0, 10); got != nil {
		t.Errorf("hits of no conversations = %v", got)
	}

	for _, key := range []string{a, testConvKey} {
		single, err := db.SearchMessagesIn(ctx, []string{key}, []string{"草莓"}, search.Range{}, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := db.SearchMessages(ctx, key, []string{"草莓"}, search.Range{}, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		for i := range single {
			if single[i].ConversationKey != key {
				t.Errorf("hit %+v of %s names another conversation", single[i], key)
			}
			single[i].ConversationKey = ""
		}
		if !reflect.DeepEqual(single, plain) {
			t.Errorf("SearchMessagesIn([%s]) = %+v, want SearchMessages' %+v", key, single, plain)
		}
	}
	if plain, _ := db.SearchMessages(ctx, testConvKey, []string{"草莓"}, search.Range{}, 0, 10); len(plain) != 1 ||
		plain[0].ConversationKey != "" {
		t.Errorf("SearchMessages = %+v, want the group's one hit without a conversation key", plain)
	}
}
