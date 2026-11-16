package panel

import (
	"context"
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

func TestConversation_PanelChatsReportSharedScope(t *testing.T) {
	h := newAutonomyHarness(t)
	ctx := context.Background()
	a := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	b := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	platform := h.seedConv("qq", conloomapi.ConvPrivate, "private_20001")
	shared := dataBot + ":panel:private:~shared"

	want := map[string]string{
		a.Key: shared, b.Key: shared, platform.Key: platform.Key,
		writeGroup.Key: writeGroup.Key, writeChannel.Key: writeChannel.Key,
	}
	var list struct {
		Conversations []convResp `json:"conversations"`
	}
	h.getJSON("/panel/api/conversations", &list)
	if len(list.Conversations) != len(want) {
		t.Fatalf("conversations = %+v, want %d", list.Conversations, len(want))
	}
	for _, c := range list.Conversations {
		if c.MemoryScope != want[c.Key] {
			t.Errorf("listed memory_scope of %s = %q, want %q", c.Key, c.MemoryScope, want[c.Key])
		}
	}
	for key, scope := range want {
		var one convResp
		h.getJSON(convPath(key), &one)
		if one.MemoryScope != scope {
			t.Errorf("memory_scope of %s = %q, want %q", key, one.MemoryScope, scope)
		}
	}

	autonomy := func(conv store.ConversationRecord) conloomapi.AutonomyState {
		t.Helper()
		var st conloomapi.AutonomyState
		h.getJSON(convPath(conv.Key, "autonomy"), &st)
		return st
	}
	if w := h.write(http.MethodPut, convPath(a.Key, "autonomy", "buffs", "心情"),
		conloomapi.AutonomyBarPut{Value: intPtr(20), Reason: "在 A 里调低"}); w.Code != http.StatusNoContent {
		t.Fatalf("PUT bar in A: %d %s", w.Code, w.Body)
	}
	until := h.now.Add(20 * time.Minute)
	if err := h.db.SetStatus(ctx, store.ConversationStatus{ConversationKey: shared, OriginConversationKey: a.Key, Activity: "洗澡",
		StartedAt: h.now.Add(-10 * time.Minute), UntilAt: until},
		&store.ConversationTimer{BotInstanceID: dataBot, PlatformUserID: "20001", DueAt: until}); err != nil {
		t.Fatal(err)
	}
	sa, sb := autonomy(a), autonomy(b)
	if sb.Status == nil || sb.Status.Activity != "洗澡" || len(sb.Bars) == 0 || sb.Bars[0].Name != "心情" || sb.Bars[0].Value != 20 ||
		len(sb.Events) != 1 || sb.Events[0].Reason != "在 A 里调低" {
		t.Fatalf("B's 自主 view = %+v, want the status and the bar change of A", sb)
	}
	if !reflect.DeepEqual(sa.Status, sb.Status) || !reflect.DeepEqual(sa.Bars, sb.Bars) || !reflect.DeepEqual(sa.Events, sb.Events) {
		t.Errorf("status and bars differ:\nA %+v\nB %+v", sa, sb)
	}
	if len(sa.Proactive.Timers) != 1 || sa.Proactive.Timers[0].Kind != string(store.TimerStatusReturn) || len(sb.Proactive.Timers) != 0 {
		t.Errorf("timers A %+v, B %+v, want the status return in A only", sa.Proactive.Timers, sb.Proactive.Timers)
	}
	if sp := autonomy(platform); sp.Status != nil || len(sp.Events) != 0 || sp.Bars[0].Value != 60 {
		t.Errorf("platform private chat = %+v, want none of the panel scope", sp)
	}

	if w := h.write(http.MethodDelete, convPath(b.Key, "autonomy", "status"), nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE status in B: %d %s", w.Code, w.Body)
	}
	if st := autonomy(a); st.Status != nil || len(st.Proactive.Timers) != 0 {
		t.Errorf("A after ending the status in B = %+v, want no status and no return", st)
	}
}

func TestConversation_PanelChatsReportMemoryInEffect(t *testing.T) {
	h := newWriteHarness(t, nil)
	a := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	b := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	platform := h.seedConv("qq", conloomapi.ConvPrivate, "private_20001")
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	h.seedMessage(a, "我在学钢琴", at, false)
	h.seedMessage(b, "我养了一只猫", at.Add(time.Minute), false)

	setMemory := func(conv store.ConversationRecord, enabled bool, noteMax int) {
		t.Helper()
		p := policyWith(func(p *conloomapi.GroupPolicy) {
			p.Memory.Enabled = enabled
			p.Memory.NoteMaxChars = noteMax
		})
		if w := h.write(http.MethodPut, convPath(conv.Key, "policy"), conloomapi.PolicyPut{Policy: p, MemoryPresent: true}); w.Code != http.StatusOK {
			t.Fatalf("PUT policy of %s: %d %s", conv.Key, w.Code, w.Body)
		}
	}
	listed := func() map[string]bool {
		t.Helper()
		var list struct {
			Conversations []convResp `json:"conversations"`
		}
		h.getJSON("/panel/api/conversations", &list)
		out := map[string]bool{}
		for _, c := range list.Conversations {
			out[c.Key] = c.MemoryEnabled
		}
		return out
	}
	check := func(when string, wantEnabled bool, wantNoteMax int) {
		t.Helper()
		got := listed()
		for _, conv := range []store.ConversationRecord{a, b} {
			var one convResp
			h.getJSON(convPath(conv.Key), &one)
			var state conloomapi.MemoryState
			h.getJSON(convPath(conv.Key, "memory"), &state)
			var pol struct {
				MemoryLimits struct {
					NoteMaxChars int `json:"note_max_chars"`
				} `json:"memory_limits"`
			}
			h.getJSON(convPath(conv.Key, "policy"), &pol)
			if got[conv.Key] != wantEnabled || one.MemoryEnabled != wantEnabled || state.Enabled != wantEnabled {
				t.Errorf("%s: memory_enabled of %s listed %v, detail %v, memory tab %v; want %v",
					when, conv.Key, got[conv.Key], one.MemoryEnabled, state.Enabled, wantEnabled)
			}
			if pol.MemoryLimits.NoteMaxChars != wantNoteMax || state.NoteMaxChars != wantNoteMax {
				t.Errorf("%s: note_max_chars of %s on the policy page %d, memory tab %d; want %d",
					when, conv.Key, pol.MemoryLimits.NoteMaxChars, state.NoteMaxChars, wantNoteMax)
			}
		}
		if got[platform.Key] {
			t.Errorf("%s: platform private chat listed with memory on", when)
		}
	}

	// A, whose policy has memory on, is not the newest chat: B's policy is in
	// effect for the shared memory.
	setMemory(a, true, 80)
	check("memory on in A only", false, 1000)
	setMemory(b, true, 90)
	check("memory on in B, the newest chat", true, 90)
}
