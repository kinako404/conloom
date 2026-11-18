package panel

import (
	"context"
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

// newChatSceneHarness is newChatHarness with a real core AutonomyService
// whose bot has skills.
func newChatSceneHarness(t *testing.T, skills ...string) (*writeHarness, *core.AutonomyService) {
	t.Helper()
	chat := &fakeChat{busy: map[string]bool{}}
	var svc *core.AutonomyService
	h := newWriteHarness(t, func(d *Deps) {
		d.Chat = chat
		bots := []config.BotConfig{{ID: dataBot, Platform: "qq", Name: "织语", PrivateTemplate: "default", GroupTemplate: "default",
			Skills: skills}}
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
		svc = o.Autonomy()
		d.Autonomy = svc
	})
	chat.h = h.storeHarness
	return h, svc
}

type chatSceneResp struct {
	Messages []chatMsgResp             `json:"messages"`
	Scene    *conloomapi.AutonomyScene `json:"scene"`
}

func (h *writeHarness) chatScene(key string) chatSceneResp {
	h.t.Helper()
	var resp chatSceneResp
	h.getJSON(convPath(key, "chat"), &resp)
	return resp
}

func TestGetChat_Scene(t *testing.T) {
	h, svc := newChatSceneHarness(t, "scene")
	chat := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	h.seedPanelMessage(chat, "在吗", h.now.Add(-time.Minute))

	got := h.chatScene(chat.Key)
	if got.Scene == nil || !got.Scene.Enabled || got.Scene.Mode != conloomapi.SceneOnline {
		t.Fatalf("scene = %+v, want enabled and online", got.Scene)
	}
	if err := svc.SetScene(context.Background(), chat.Key, conloomapi.SceneInPerson, "食堂", store.BuffSourcePanel, "panel:"+testUser); err != nil {
		t.Fatalf("SetScene: %v", err)
	}
	got = h.chatScene(chat.Key)
	if got.Scene == nil || !got.Scene.Enabled || got.Scene.Mode != conloomapi.SceneInPerson || got.Scene.Place != "食堂" {
		t.Errorf("scene = %+v, want in person at 食堂", got.Scene)
	}
	if len(got.Messages) != 1 {
		t.Errorf("messages = %+v, want the one message", got.Messages)
	}

	group := h.seedConv(store.PlatformPanel, conloomapi.ConvGroup, "panel_g")
	h.seedMessage(group, "大家好", h.now.Add(-time.Minute), false)
	if body := string(h.getJSON(convPath(group.Key, "chat"), nil)); !strings.Contains(body, `"scene":null`) {
		t.Errorf("group chat body %s, want \"scene\":null", body)
	}
}

func TestGetChat_SceneWithoutAutonomy(t *testing.T) {
	h, _ := newChatHarness(t)
	chat := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	h.seedPanelMessage(chat, "在吗", h.now.Add(-time.Minute))
	body := string(h.getJSON(convPath(chat.Key, "chat"), nil))
	if !strings.Contains(body, `"scene":null`) {
		t.Errorf("body %s, want \"scene\":null without the autonomy service", body)
	}
	if got := h.chatScene(chat.Key); len(got.Messages) != 1 {
		t.Errorf("messages = %+v, want the one message", got.Messages)
	}
}

func TestGetChat_SceneOfBotWithoutSkill(t *testing.T) {
	h, _ := newChatSceneHarness(t)
	chat := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	got := h.chatScene(chat.Key)
	if got.Scene == nil || got.Scene.Enabled || got.Scene.Mode != conloomapi.SceneOnline {
		t.Errorf("scene = %+v, want a scene that is not enabled", got.Scene)
	}
}
