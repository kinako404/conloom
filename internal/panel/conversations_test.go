package panel

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type convResp struct {
	Key                    string     `json:"key"`
	BotInstanceID          string     `json:"bot_instance_id"`
	BotName                string     `json:"bot_name"`
	Platform               string     `json:"platform"`
	Type                   string     `json:"type"`
	PlatformConversationID string     `json:"platform_conversation_id"`
	MessageCount           int        `json:"message_count"`
	LastMessageAt          *time.Time `json:"last_message_at"`
	Paused                 bool       `json:"paused"`
	ProactiveEnabled       bool       `json:"proactive_enabled"`
	MemoryEnabled          bool       `json:"memory_enabled"`
	PersonaTemplate        *string    `json:"persona_template"`
	PersonaVersion         *int       `json:"persona_version"`
}

func TestConversations_ListAndDetail(t *testing.T) {
	dh := newDataHarness(t)
	ctx := context.Background()

	want := `{"conversations":[],"bots":[{"id":"qq-main","name":"织语","platform":"qq"}]}` + "\n"
	if body := string(dh.getJSON("/panel/api/conversations", nil)); body != want {
		t.Fatalf("empty store: body %q, want %q", body, want)
	}

	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")
	private := dh.seedConv("qq", conloomapi.ConvPrivate, "20001")
	base := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	dh.seedMessage(group, "早上好", base, false)
	dh.seedMessage(group, "周末去爬山", base.Add(time.Minute), false)
	dh.seedMessage(group, "好呀", base.Add(2*time.Minute), true)

	p := conloomapi.DefaultGroupPolicy()
	p.Proactive.Enabled = false
	p.Memory.Enabled = true
	if _, err := dh.policies.Update(ctx, group.Key, conloomapi.PolicyPut{Policy: p, ProactivePresent: true, MemoryPresent: true}, "panel:admin"); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	if _, err := dh.policies.Pause(ctx, group.Key, "panel:admin"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := dh.personas.Update(ctx, group.Key, "lighthouse", conloomapi.PersonaPut{Template: "default"}, "panel:admin"); err != nil {
		t.Fatalf("persona v1: %v", err)
	}
	if _, err := dh.personas.Update(ctx, group.Key, "lighthouse", conloomapi.PersonaPut{Template: "lighthouse"}, "panel:admin"); err != nil {
		t.Fatalf("persona v2: %v", err)
	}

	var list struct {
		Conversations []convResp `json:"conversations"`
	}
	dh.getJSON("/panel/api/conversations", &list)
	if len(list.Conversations) != 2 {
		t.Fatalf("conversations = %+v, want 2", list.Conversations)
	}
	g, pr := list.Conversations[0], list.Conversations[1]
	if g.Key != group.Key || g.BotInstanceID != dataBot || g.BotName != "织语" || g.Platform != "qq" ||
		g.Type != conloomapi.ConvGroup || g.PlatformConversationID != "10001" || g.MessageCount != 3 ||
		g.LastMessageAt == nil || !g.LastMessageAt.Equal(base.Add(2*time.Minute)) {
		t.Errorf("group summary = %+v", g)
	}
	if !g.Paused || g.ProactiveEnabled || !g.MemoryEnabled {
		t.Errorf("group paused/proactive/memory = %v/%v/%v, want true/false/true", g.Paused, g.ProactiveEnabled, g.MemoryEnabled)
	}
	if g.PersonaTemplate == nil || *g.PersonaTemplate != "lighthouse" || g.PersonaVersion == nil || *g.PersonaVersion != 2 {
		t.Errorf("group persona = %v v%v, want lighthouse v2", g.PersonaTemplate, g.PersonaVersion)
	}

	if pr.Key != private.Key || pr.Type != conloomapi.ConvPrivate || pr.MessageCount != 0 || pr.LastMessageAt != nil {
		t.Errorf("private summary = %+v", pr)
	}
	if pr.Paused || !pr.ProactiveEnabled || pr.MemoryEnabled || pr.PersonaTemplate != nil || pr.PersonaVersion != nil {
		t.Errorf("private defaults = %+v, want running, proactive on, memory off, no persona", pr)
	}

	var one convResp
	dh.getJSON(convPath(group.Key), &one)
	if one.Key != g.Key || one.MessageCount != g.MessageCount || !one.LastMessageAt.Equal(*g.LastMessageAt) ||
		one.Paused != g.Paused || *one.PersonaVersion != *g.PersonaVersion || one.BotName != g.BotName {
		t.Errorf("detail = %+v, want %+v", one, g)
	}
	var none convResp
	dh.getJSON(convPath(private.Key), &none)
	if none.MessageCount != 0 || none.LastMessageAt != nil || none.PersonaVersion != nil {
		t.Errorf("private detail = %+v", none)
	}

	if n, err := dh.db.ListPersonaVersions(ctx, private.Key); err != nil || len(n) != 0 {
		t.Errorf("listing wrote persona versions for the private chat: %v, %v", n, err)
	}
}

func TestConversation_KeyRoundTrip(t *testing.T) {
	dh := newDataHarness(t)
	tg := dh.seedConv("telegram", conloomapi.ConvGroup, "-1001234")
	if tg.Key != "qq-main:telegram:group:-1001234" {
		t.Fatalf("key = %q", tg.Key)
	}
	pct := dh.seedConv("qq", conloomapi.ConvGroup, "100%25 a")
	dh.seedMessage(tg, "你好", time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), false)

	for _, conv := range []store.ConversationRecord{tg, pct} {
		var got convResp
		dh.getJSON(convPath(conv.Key), &got)
		if got.Key != conv.Key || got.PlatformConversationID != conv.PlatformConversationID {
			t.Errorf("GET %s = %+v, want key %q", convPath(conv.Key), got, conv.Key)
		}
	}
	// As encodeURIComponent sends it.
	var colons convResp
	dh.getJSON("/panel/api/conversations/qq-main%3Atelegram%3Agroup%3A-1001234", &colons)
	if colons.Key != tg.Key {
		t.Errorf("colon-escaped key = %+v, want %q", colons, tg.Key)
	}
	var msgs struct {
		Messages []msgResp `json:"messages"`
	}
	dh.getJSON(convPath(tg.Key, "messages"), &msgs)
	if len(msgs.Messages) != 1 || msgs.Messages[0].Text != "你好" {
		t.Errorf("messages of %s = %+v", tg.Key, msgs.Messages)
	}

	dh.getErr(convPath("qq-main:telegram:group:-999"), http.StatusNotFound, conloomapi.CodeNotFound)
	dh.getErr(convPath("qq-main:telegram:group:-999", "messages"), http.StatusNotFound, conloomapi.CodeNotFound)
	dh.getErr(convPath("qq-main:telegram:group:-999", "persona"), http.StatusNotFound, conloomapi.CodeNotFound)
	dh.getErr(convPath("qq-main:telegram:group:-999", "policy"), http.StatusNotFound, conloomapi.CodeNotFound)
	dh.getErr(convPath("qq-main:telegram:group:-999", "memory"), http.StatusNotFound, conloomapi.CodeNotFound)

	if w := dh.do(http.MethodGet, convPath(tg.Key), ""); w.Code != http.StatusUnauthorized {
		t.Errorf("without a session: status %d, want 401", w.Code)
	}
}
