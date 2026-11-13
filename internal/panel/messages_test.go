package panel

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type msgResp struct {
	Seq        int64     `json:"seq"`
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	SenderID   string    `json:"sender_id"`
	SenderName string    `json:"sender_name"`
	FromBot    bool      `json:"from_bot"`
	Text       string    `json:"text"`
	ReplyTo    string    `json:"reply_to"`
}

type browseResp struct {
	Messages   []msgResp `json:"messages"`
	NextBefore *int64    `json:"next_before"`
}

type searchResp struct {
	Messages   []msgResp `json:"messages"`
	NextOffset *int      `json:"next_offset"`
}

func msgIDs(msgs []msgResp) []string {
	out := []string{}
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

func TestMessages_BrowseAndSearch(t *testing.T) {
	dh := newDataHarness(t)
	cst := time.FixedZone("CST", 8*3600)
	dh.srv.d.Location = cst
	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")
	other := dh.seedConv("qq", conloomapi.ConvGroup, "10002")

	day1 := time.Date(2026, 11, 12, 10, 0, 0, 0, cst)
	day2 := time.Date(2026, 11, 13, 1, 0, 0, 0, cst) // 2026-11-12 17:00 UTC
	m1 := dh.seedMessage(group, "周末去爬山吗", day1, false)
	m2 := dh.seedMessage(group, "<script>alert(1)</script> 好呀", day1.Add(time.Minute), true)
	m3 := dh.seedMessage(group, "这个周末下雨", day1.Add(2*time.Minute), false)
	dh.seedMessage(other, "周末加班", day1.Add(3*time.Minute), false)
	dh.seedMessage(other, "周末快乐", day2, false)
	m4 := dh.seedMessage(group, "周末记得带伞", day2, false)
	m5 := dh.seedMessage(group, "晚安", day2.Add(time.Minute), false)

	var page browseResp
	dh.get(convPath(group.Key, "messages")+"?limit=2", &page)
	if got := msgIDs(page.Messages); fmt.Sprint(got) != fmt.Sprint([]string{m5.ID, m4.ID}) {
		t.Fatalf("page 1 = %v, want %v", got, []string{m5.ID, m4.ID})
	}
	if page.NextBefore == nil || *page.NextBefore != m4.Seq {
		t.Fatalf("page 1 next_before = %v, want %d", page.NextBefore, m4.Seq)
	}
	dh.get(convPath(group.Key, "messages")+fmt.Sprintf("?before=%d&limit=2", *page.NextBefore), &page)
	if got := msgIDs(page.Messages); fmt.Sprint(got) != fmt.Sprint([]string{m3.ID, m2.ID}) || page.NextBefore == nil || *page.NextBefore != m2.Seq {
		t.Fatalf("page 2 = %v next %v, want %v next %d", got, page.NextBefore, []string{m3.ID, m2.ID}, m2.Seq)
	}
	dh.get(convPath(group.Key, "messages")+fmt.Sprintf("?before=%d&limit=2", *page.NextBefore), &page)
	if got := msgIDs(page.Messages); fmt.Sprint(got) != fmt.Sprint([]string{m1.ID}) || page.NextBefore != nil {
		t.Fatalf("page 3 = %v next %v, want [%s] and no next", got, page.NextBefore, m1.ID)
	}
	if body := string(dh.get(convPath(group.Key, "messages")+fmt.Sprintf("?before=%d", m1.Seq), nil)); body != "{\"messages\":[],\"next_before\":null}\n" {
		t.Errorf("before the first message: body %q", body)
	}

	var all browseResp
	dh.get(convPath(group.Key, "messages"), &all)
	if len(all.Messages) != 5 || all.NextBefore != nil {
		t.Fatalf("default page = %v next %v, want all 5 and no next", msgIDs(all.Messages), all.NextBefore)
	}
	bot := all.Messages[3]
	want := msgResp{Seq: m2.Seq, ID: m2.ID, At: m2.OccurredAt, SenderID: "30001", SenderName: "织语", FromBot: true, Text: m2.Text}
	if bot != want {
		t.Errorf("bot message = %+v, want %+v", bot, want)
	}
	if user := all.Messages[4]; user.SenderID != "20001" || user.SenderName != "小明" || user.FromBot || user.Text != "周末去爬山吗" {
		t.Errorf("user message = %+v", user)
	}

	groupIDs := map[string]bool{m1.ID: true, m2.ID: true, m3.ID: true, m4.ID: true, m5.ID: true}
	q := "?q=" + "%E5%91%A8%E6%9C%AB" // 周末
	var hits searchResp
	dh.get(convPath(group.Key, "messages")+q+"&limit=2", &hits)
	if len(hits.Messages) != 2 || hits.NextOffset == nil || *hits.NextOffset != 2 {
		t.Fatalf("search page 1 = %v next %v, want 2 hits and next_offset 2", msgIDs(hits.Messages), hits.NextOffset)
	}
	found := msgIDs(hits.Messages)
	dh.get(convPath(group.Key, "messages")+q+"&limit=2&offset=2", &hits)
	if len(hits.Messages) != 1 || hits.NextOffset != nil {
		t.Fatalf("search page 2 = %v next %v, want 1 hit and no next", msgIDs(hits.Messages), hits.NextOffset)
	}
	found = append(found, msgIDs(hits.Messages)...)
	seen := map[string]bool{}
	for _, id := range found {
		if !groupIDs[id] || id == m2.ID || id == m5.ID || seen[id] {
			t.Errorf("search found %v, want m1, m3 and m4 of the group once each", found)
			break
		}
		seen[id] = true
	}

	dh.get(convPath(group.Key, "messages")+q+"&from=2026-11-13&to=2026-11-13", &hits)
	if got := msgIDs(hits.Messages); fmt.Sprint(got) != fmt.Sprint([]string{m4.ID}) || hits.NextOffset != nil {
		t.Errorf("search on 2026-11-13 (server time) = %v, want [%s]", got, m4.ID)
	}
	dh.get(convPath(group.Key, "messages")+q+"&to=2026-11-12", &hits)
	if len(hits.Messages) != 2 {
		t.Errorf("search up to 2026-11-12 = %v, want m1 and m3", msgIDs(hits.Messages))
	}
	if body := string(dh.get(convPath(group.Key, "messages")+q+"&from=2026-11-14", nil)); body != "{\"messages\":[],\"next_offset\":null}\n" {
		t.Errorf("search after the last message: body %q", body)
	}

	for _, bad := range []string{
		q + "&from=2026/11/13", q + "&to=13-11-2026", q + "&from=2026-11-14&to=2026-11-13",
		q + "&offset=-1", q + "&offset=x", q + "&limit=0", "?limit=abc", "?before=x", "?before=-2",
	} {
		dh.getErr(convPath(group.Key, "messages")+bad, http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
}

func TestMessages_SearchBoundsSeqsByLimit(t *testing.T) {
	dh := newDataHarness(t)
	var calls []int
	dh.srv.d.Store = &seqCountingStore{Store: dh.st, calls: &calls}
	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")
	for i := range 5 {
		dh.seedMessage(group, fmt.Sprintf("周末 %d", i), time.Date(2026, 11, 12, 9, i, 0, 0, time.UTC), false)
	}
	var hits searchResp
	dh.get(convPath(group.Key, "messages")+"?q=%E5%91%A8%E6%9C%AB&limit=3", &hits)
	if len(hits.Messages) != 3 || fmt.Sprint(calls) != "[3]" || hits.NextOffset == nil || *hits.NextOffset != 3 {
		t.Errorf("hits %d next %v, MessagesBySeq sizes %v; want 3 hits from one call of 3 seqs, next_offset 3",
			len(hits.Messages), hits.NextOffset, calls)
	}
}

// seqCountingStore records the number of seqs asked of MessagesBySeq, and
// returns more search hits than asked for.
type seqCountingStore struct {
	Store
	calls *[]int
}

func (s *seqCountingStore) SearchMessages(ctx context.Context, convKey string, terms []string, r search.Range, offset, limit int) ([]store.MessageHit, error) {
	return s.Store.SearchMessages(ctx, convKey, terms, r, offset, limit+2)
}

func (s *seqCountingStore) MessagesBySeq(ctx context.Context, convKey string, seqs []int64) (map[int64]store.MessageRecord, error) {
	*s.calls = append(*s.calls, len(seqs))
	return s.Store.MessagesBySeq(ctx, convKey, seqs)
}
