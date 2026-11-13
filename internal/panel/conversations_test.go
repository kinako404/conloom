package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const dataBot = "qq-main"

// dataHarness is a harness whose server reads a real SQLite store through the
// real policy, persona and memory services, with a logged-in session.
type dataHarness struct {
	*harness
	st       *sqlite.DB
	policies *policy.Service
	personas *persona.Service
	memAdmin *memory.Admin
	cookie   *http.Cookie
	pmid     int
}

func lighthousePersona() conloomapi.PersonaDoc {
	p := conloomapi.DefaultPersona()
	p.PersonaName = "灯塔"
	return p
}

func newDataHarness(t *testing.T) *dataHarness {
	t.Helper()
	h := newHarness(t, nil)
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.cfg.Bots = []config.BotConfig{{ID: dataBot, Platform: "qq", Name: "织语", PersonaTemplate: "lighthouse"}}
	h.cfg.PersonaTemplates = map[string]conloomapi.PersonaDoc{
		"default": conloomapi.DefaultPersona(), "lighthouse": lighthousePersona(),
	}
	dh := &dataHarness{harness: h, st: st}
	dh.policies = policy.NewService(st, conloomapi.DefaultGroupPolicy())
	dh.personas = persona.NewService(st, h.cfg.PersonaTemplates)
	dh.memAdmin = memory.NewAdmin(memory.AdminDeps{Store: st, Policies: dh.policies, Now: func() time.Time { return h.now }})
	d := h.srv.d
	d.Store, d.Policies, d.Personas, d.MemoryAdmin = st, dh.policies, dh.personas, dh.memAdmin
	h.srv = New(d)
	h.h = h.srv.Handler()
	dh.cookie, _ = h.loginOK()
	return dh
}

// seedConv stores a conversation of dataBot with conversation ID convID.
func (dh *dataHarness) seedConv(platform, convType, convID string) store.ConversationRecord {
	dh.t.Helper()
	conv := store.ConversationRecord{
		BotInstanceID: dataBot, ID: convID, Key: store.ConversationKey(dataBot, platform, convType, convID),
		Type: convType, Platform: platform, PlatformConversationID: convID,
	}
	dh.pmid++
	_, err := dh.st.Ingest(context.Background(), store.Ingest{
		Event:        store.EventRecord{BotInstanceID: dataBot, IdempotencyKey: fmt.Sprintf("seed:%d", dh.pmid), EventID: fmt.Sprintf("evt_%d", dh.pmid)},
		Conversation: &conv,
	})
	if err != nil {
		dh.t.Fatalf("seed conversation: %v", err)
	}
	return conv
}

// seedMessage stores a message in conv from user 20001 (小明), or from the
// bot when fromBot, and returns it with its seq.
func (dh *dataHarness) seedMessage(conv store.ConversationRecord, text string, at time.Time, fromBot bool) store.MessageRecord {
	dh.t.Helper()
	dh.pmid++
	m := store.MessageRecord{
		ID: fmt.Sprintf("msg_%d", dh.pmid), BotInstanceID: dataBot, ConversationKey: conv.Key, Platform: conv.Platform,
		PlatformMessageID: fmt.Sprint(dh.pmid), ActorID: "user_20001", PlatformUserID: "20001", DisplayName: "小明",
		Text: text, FromBot: fromBot, OccurredAt: at.UTC(), CreatedAt: at.UTC(),
	}
	if fromBot {
		m.ActorID, m.PlatformUserID, m.DisplayName = "bot_"+dataBot, "30001", "织语"
	}
	_, err := dh.st.Ingest(context.Background(), store.Ingest{
		Event:   store.EventRecord{BotInstanceID: dataBot, IdempotencyKey: fmt.Sprintf("seed:%d", dh.pmid), EventID: fmt.Sprintf("evt_%d", dh.pmid)},
		Message: &m,
	})
	if err != nil {
		dh.t.Fatalf("seed message: %v", err)
	}
	got, err := dh.st.GetMessage(context.Background(), dataBot, m.ID)
	if err != nil {
		dh.t.Fatalf("read seeded message: %v", err)
	}
	return got
}

// get requests path as the logged-in user and decodes a 200 response into v.
func (dh *dataHarness) get(path string, v any) []byte {
	dh.t.Helper()
	w := dh.do(http.MethodGet, path, "", withCookie(dh.cookie))
	if w.Code != http.StatusOK {
		dh.t.Fatalf("GET %s: status %d, body %s", path, w.Code, w.Body)
	}
	if v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			dh.t.Fatalf("GET %s: decode %s: %v", path, w.Body, err)
		}
	}
	return w.Body.Bytes()
}

// getErr requests path as the logged-in user and checks the error response.
func (dh *dataHarness) getErr(path string, status int, code string) {
	dh.t.Helper()
	errorBody(dh.t, dh.do(http.MethodGet, path, "", withCookie(dh.cookie)), status, code)
}

func convPath(key string, rest ...string) string {
	p := "/panel/api/conversations/" + url.PathEscape(key)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

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

	if body := string(dh.get("/panel/api/conversations", nil)); body != "{\"conversations\":[]}\n" {
		t.Fatalf("empty store: body %q, want {\"conversations\":[]}", body)
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
	dh.get("/panel/api/conversations", &list)
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
	dh.get(convPath(group.Key), &one)
	if one.Key != g.Key || one.MessageCount != g.MessageCount || !one.LastMessageAt.Equal(*g.LastMessageAt) ||
		one.Paused != g.Paused || *one.PersonaVersion != *g.PersonaVersion || one.BotName != g.BotName {
		t.Errorf("detail = %+v, want %+v", one, g)
	}
	var none convResp
	dh.get(convPath(private.Key), &none)
	if none.MessageCount != 0 || none.LastMessageAt != nil || none.PersonaVersion != nil {
		t.Errorf("private detail = %+v", none)
	}

	if n, err := dh.st.ListPersonaVersions(ctx, private.Key); err != nil || len(n) != 0 {
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
		dh.get(convPath(conv.Key), &got)
		if got.Key != conv.Key || got.PlatformConversationID != conv.PlatformConversationID {
			t.Errorf("GET %s = %+v, want key %q", convPath(conv.Key), got, conv.Key)
		}
	}
	// As encodeURIComponent sends it.
	var colons convResp
	dh.get("/panel/api/conversations/qq-main%3Atelegram%3Agroup%3A-1001234", &colons)
	if colons.Key != tg.Key {
		t.Errorf("colon-escaped key = %+v, want %q", colons, tg.Key)
	}
	var msgs struct {
		Messages []msgResp `json:"messages"`
	}
	dh.get(convPath(tg.Key, "messages"), &msgs)
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
