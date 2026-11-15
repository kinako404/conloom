package panel

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestMemory_PanelChatsShareScope(t *testing.T) {
	h := newWriteHarness(t, nil)
	a := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	b := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	platform := h.seedConv("qq", conloomapi.ConvPrivate, "private_20001")
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	h.seedMessage(a, "我在学钢琴", at, false)
	h.seedMessage(b, "我养了一只猫", at.Add(time.Minute), false)
	h.seedMessage(platform, "你好", at.Add(2*time.Minute), false)

	view := func(conv store.ConversationRecord) conloomapi.MemoryState {
		t.Helper()
		var state conloomapi.MemoryState
		h.getJSON(convPath(conv.Key, "memory"), &state)
		if state.ConversationID != conv.ID {
			t.Errorf("memory of %s names conversation %q", conv.Key, state.ConversationID)
		}
		state.ConversationID = ""
		return state
	}
	revisions := func(conv store.ConversationRecord) string {
		t.Helper()
		return string(h.getJSON(convPath(conv.Key, "memory", "revisions")+"?user_id=20001", nil))
	}

	note, affinity := "喜欢钢琴", 45
	if w := h.write(http.MethodPut, convPath(a.Key, "memory", "members", "20001"),
		conloomapi.MemberMemoryPut{Note: &note, Affinity: &affinity}); w.Code != http.StatusOK {
		t.Fatalf("PUT member memory in A: %d %s", w.Code, w.Body)
	}
	va, vb := view(a), view(b)
	if len(vb.Members) != 1 || vb.Members[0].Note != note || vb.Members[0].Affinity == nil || *vb.Members[0].Affinity != affinity {
		t.Fatalf("memory of B = %+v, want the member set in A", vb)
	}
	if !reflect.DeepEqual(va, vb) {
		t.Errorf("memory views differ:\nA %+v\nB %+v", va, vb)
	}
	if ra, rb := revisions(a), revisions(b); ra != rb || ra == "{\"revisions\":[]}\n" {
		t.Errorf("revisions differ or are missing:\nA %s\nB %s", ra, rb)
	}
	if got := view(platform); len(got.Members) != 0 {
		t.Errorf("platform private chat memory = %+v, want none of the panel scope", got)
	}

	on := policyWith(func(p *conloomapi.GroupPolicy) { p.Memory.Enabled = true })
	if w := h.write(http.MethodPut, convPath(b.Key, "policy"), conloomapi.PolicyPut{Policy: on}); w.Code != http.StatusOK {
		t.Fatalf("enable memory in B: %d %s", w.Code, w.Body)
	}
	if !view(a).Enabled {
		t.Error("memory of A is off, though B, the newest chat, has it on")
	}
	if w := h.write(http.MethodPost, convPath(a.Key, "memory", "consolidate"), nil); w.Code != http.StatusAccepted {
		t.Fatalf("consolidate from A while B is the newest chat: %d %s", w.Code, w.Body)
	}
	h.seedMessage(a, "今天弹了一首曲子", at.Add(3*time.Minute), false)
	errorBody(t, h.write(http.MethodPost, convPath(b.Key, "memory", "consolidate"), nil), http.StatusConflict, conloomapi.CodeMemoryDisabled)

	if w := h.write(http.MethodDelete, convPath(b.Key, "memory", "members", "20001"), nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE member memory in B: %d %s", w.Code, w.Body)
	}
	if got := view(a); len(got.Members) != 0 {
		t.Errorf("memory of A after forgetting in B = %+v", got)
	}
	var revs struct {
		Revisions []json.RawMessage `json:"revisions"`
	}
	if err := json.Unmarshal([]byte(revisions(a)), &revs); err != nil || len(revs.Revisions) != 0 {
		t.Errorf("revisions of A after forgetting in B = %s, %v", revisions(a), err)
	}
}
