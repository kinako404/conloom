package panel

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/botdir"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// newSceneHarness is newAutonomyHarness whose bot enables the scene skill.
func newSceneHarness(t *testing.T) *writeHarness {
	t.Helper()
	return newWriteHarness(t, func(d *Deps) {
		bots := []config.BotConfig{{ID: dataBot, Platform: "qq", Name: "织语", PrivateTemplate: "default", GroupTemplate: "default",
			Skills: []string{"scene"}}}
		d.Config.Bots, d.Bots = bots, botdir.New(bots)
		o, err := core.New(core.Deps{
			Store: d.Store.(*sqlite.DB), Router: router.New(nil, router.BreakerConfig{}, time.Now),
			Admission: admission.New(nil), Personas: d.Personas, Policies: d.Policies,
			Reviewer: review.New(nil, review.Options{}), Config: d.Config, Bots: d.Bots, Location: time.UTC, Now: d.Now,
		})
		if err != nil {
			t.Fatalf("core.New: %v", err)
		}
		t.Cleanup(o.Close)
		d.Autonomy = o.Autonomy()
	})
}

func TestPanelAutonomy_Scene(t *testing.T) {
	h := newSceneHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(private, "在吗", h.now.Add(-time.Hour), false)
	path := convPath(private.Key, "autonomy")
	scene := convPath(private.Key, "autonomy", "scene")

	var st conloomapi.AutonomyState
	h.getJSON(path, &st)
	if st.Scene != (conloomapi.AutonomyScene{Enabled: true, Mode: conloomapi.SceneOnline}) {
		t.Fatalf("scene = %+v, want enabled and online", st.Scene)
	}

	if w := h.do(http.MethodPut, scene, `{"mode":"in_person","place":""}`, withCookie(h.cookie)); w.Code != http.StatusForbidden {
		t.Errorf("PUT without CSRF: status %d, want 403", w.Code)
	}
	if w := h.write(http.MethodPut, scene, `{"mode":"in_person","place":""}`); w.Code != http.StatusNoContent {
		t.Fatalf("PUT scene: status %d, body %s", w.Code, w.Body)
	}
	if raw := h.get(path); !strings.Contains(raw.Body.String(), `"scene":{"enabled":true,"mode":"in_person","place":"","since_at":"`) {
		t.Errorf("GET = %s, want the in-person scene", raw.Body)
	}
	h.getJSON(path, &st)
	if !st.Scene.Enabled || st.Scene.Mode != conloomapi.SceneInPerson || st.Scene.Place != "" || !st.Scene.SinceAt.Equal(h.now) ||
		st.Scene.IdleOnline {
		t.Errorf("scene after PUT = %+v, want in person since %v", st.Scene, h.now)
	}
	if w := h.write(http.MethodPut, scene, conloomapi.AutonomyScenePut{Mode: conloomapi.SceneInPerson, Place: "<b>食堂</b>"}); w.Code != http.StatusNoContent {
		t.Fatalf("PUT scene with a place: status %d, body %s", w.Code, w.Body)
	}
	if h.getJSON(path, &st); st.Scene.Place != "<b>食堂</b>" {
		t.Errorf("place = %q, want it stored as written", st.Scene.Place)
	}

	if e := errorBody(t, h.write(http.MethodPut, scene, `{"mode":"offline"}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent); !strings.Contains(e.Message, "offline") ||
		strings.Contains(e.Message, "core:") {
		t.Errorf("unknown mode: message %q", e.Message)
	}
	long := `{"mode":"in_person","place":"` + strings.Repeat("食", conloomapi.ScenePlaceMaxRunes+1) + `"}`
	if e := errorBody(t, h.write(http.MethodPut, scene, long), http.StatusBadRequest, conloomapi.CodeInvalidEvent); !strings.Contains(e.Message, "16") ||
		strings.Contains(e.Message, "core:") {
		t.Errorf("long place: message %q", e.Message)
	}
	errorBody(t, h.write(http.MethodPut, scene, `{"mode":"online","since":1}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.write(http.MethodPut, convPath(writeGroup.Key, "autonomy", "scene"), `{"mode":"online"}`),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	missing := store.ConversationKey(dataBot, "qq", conloomapi.ConvPrivate, "29999")
	errorBody(t, h.write(http.MethodPut, convPath(missing, "autonomy", "scene"), `{"mode":"online"}`), http.StatusNotFound, conloomapi.CodeNotFound)

	audits := h.audits("autonomy.scene.set")
	if len(audits) != 2 {
		t.Fatalf("autonomy.scene.set audits = %+v, want 2", audits)
	}
	for _, a := range audits {
		if a.Actor != "panel:"+testUser || a.Target != private.Key || a.BotInstanceID != dataBot || !strings.Contains(a.Detail, `"source":"panel"`) {
			t.Errorf("audit = %+v, want the panel user's", a)
		}
	}
}

func TestPanelAutonomy_SceneOfBotWithoutSkill(t *testing.T) {
	h := newAutonomyHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(private, "在吗", h.now.Add(-time.Hour), false)
	if raw := h.get(convPath(private.Key, "autonomy")); !strings.Contains(raw.Body.String(), `"scene":{"enabled":false,"mode":"online","place":"","idle_online":false}`) {
		t.Errorf("GET = %s, want a scene that is not enabled", raw.Body)
	}
	if w := h.write(http.MethodPut, convPath(private.Key, "autonomy", "scene"), `{"mode":"in_person","place":"图书馆"}`); w.Code != http.StatusNoContent {
		t.Errorf("PUT scene of a bot without the skill: status %d, body %s", w.Code, w.Body)
	}
}

func TestPanelAutonomy_SceneUnavailable(t *testing.T) {
	h := newWriteHarness(t, nil)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	errorBody(t, h.write(http.MethodPut, convPath(private.Key, "autonomy", "scene"), `{"mode":"online"}`),
		http.StatusServiceUnavailable, conloomapi.CodeInternal)
}
