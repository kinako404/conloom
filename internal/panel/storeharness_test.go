package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// storeHarness is a harness whose server reads and writes a real SQLite store
// through the real policy, persona and memory services, logged in as
// testUser.
type storeHarness struct {
	*harness
	db       *sqlite.DB
	policies *policy.Service
	personas *persona.Service
	memAdmin *memory.Admin
	cookie   *http.Cookie
	csrf     string
	seeded   int // events stored by the seed helpers
}

// newStoreHarness serves bots with the persona templates templates; mutate,
// when not nil, adjusts the dependencies before the server is built.
func newStoreHarness(t *testing.T, bots []config.BotConfig, templates map[string]conloomapi.PersonaDoc, mutate func(*Deps)) *storeHarness {
	t.Helper()
	h := newHarness(t, nil)
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.cfg.Bots, h.cfg.PersonaTemplates = bots, templates
	sh := &storeHarness{harness: h, db: db}
	sh.policies = policy.NewService(db, conloomapi.DefaultGroupPolicy())
	sh.personas = persona.NewService(db, templates)
	sh.memAdmin = memory.NewAdmin(memory.AdminDeps{Store: db, Policies: sh.policies, Now: func() time.Time { return h.now }})
	d := h.srv.d
	d.Store, d.Policies, d.Personas, d.MemoryAdmin = db, sh.policies, sh.personas, sh.memAdmin
	if mutate != nil {
		mutate(&d)
	}
	h.srv = New(d)
	h.h = h.srv.Handler()
	sh.cookie, sh.csrf = h.loginOK()
	return sh
}

func lighthousePersona() conloomapi.PersonaDoc {
	p := conloomapi.DefaultPersona()
	p.PersonaName = "灯塔"
	return p
}

// newDataHarness serves dataBot, whose persona template is lighthouse.
func newDataHarness(t *testing.T) *storeHarness {
	t.Helper()
	return newStoreHarness(t,
		[]config.BotConfig{{ID: dataBot, Platform: "qq", Name: "织语", PersonaTemplate: "lighthouse"}},
		map[string]conloomapi.PersonaDoc{"default": conloomapi.DefaultPersona(), "lighthouse": lighthousePersona()},
		nil)
}

// ingest stores in under the seeded event n of dataBot.
func (h *storeHarness) ingest(n int, in store.Ingest) {
	h.t.Helper()
	in.Event = store.EventRecord{BotInstanceID: dataBot, IdempotencyKey: fmt.Sprintf("seed:%d", n), EventID: fmt.Sprintf("evt_%d", n)}
	if _, err := h.db.Ingest(context.Background(), in); err != nil {
		h.t.Fatalf("seed: %v", err)
	}
}

// storeConv stores conv, a conversation of dataBot.
func (h *storeHarness) storeConv(conv store.ConversationRecord) {
	h.t.Helper()
	h.seeded++
	h.ingest(h.seeded, store.Ingest{Conversation: &conv})
}

// seedConv stores a conversation of dataBot with conversation ID convID.
func (h *storeHarness) seedConv(platform, convType, convID string) store.ConversationRecord {
	h.t.Helper()
	conv := store.ConversationRecord{
		BotInstanceID: dataBot, ID: convID, Key: store.ConversationKey(dataBot, platform, convType, convID),
		Type: convType, Platform: platform, PlatformConversationID: convID,
	}
	h.storeConv(conv)
	return conv
}

// seedMessage stores a message in conv from user 20001 (小明), or from the
// bot when fromBot, and returns it with its seq.
func (h *storeHarness) seedMessage(conv store.ConversationRecord, text string, at time.Time, fromBot bool) store.MessageRecord {
	h.t.Helper()
	h.seeded++
	m := store.MessageRecord{
		ID: fmt.Sprintf("msg_%d", h.seeded), BotInstanceID: dataBot, ConversationKey: conv.Key, Platform: conv.Platform,
		PlatformMessageID: fmt.Sprint(h.seeded), ActorID: "user_20001", PlatformUserID: "20001", DisplayName: "小明",
		Text: text, FromBot: fromBot, OccurredAt: at.UTC(), CreatedAt: at.UTC(),
	}
	if fromBot {
		m.ActorID, m.PlatformUserID, m.DisplayName = "bot_"+dataBot, "30001", "织语"
	}
	h.ingest(h.seeded, store.Ingest{Message: &m})
	got, err := h.db.GetMessage(context.Background(), dataBot, m.ID)
	if err != nil {
		h.t.Fatalf("read seeded message: %v", err)
	}
	return got
}

// get requests path as the logged-in user.
func (h *storeHarness) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodGet, path, "", withCookie(h.cookie))
}

// getJSON GETs path, which must answer 200, decodes the body into v unless v
// is nil, and returns the body.
func (h *storeHarness) getJSON(path string, v any) []byte {
	h.t.Helper()
	w := h.get(path)
	if w.Code != http.StatusOK {
		h.t.Fatalf("GET %s: status %d, body %s", path, w.Code, w.Body)
	}
	if v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			h.t.Fatalf("GET %s: decode %s: %v", path, w.Body, err)
		}
	}
	return w.Body.Bytes()
}

// getErr GETs path as the logged-in user and checks the error response.
func (h *storeHarness) getErr(path string, status int, code string) {
	h.t.Helper()
	errorBody(h.t, h.get(path), status, code)
}

// convPath is the API path of the conversation key, followed by the path
// segments rest.
func convPath(key string, rest ...string) string {
	p := "/panel/api/conversations/" + url.PathEscape(key)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}
