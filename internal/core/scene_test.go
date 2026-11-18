package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	sceneOnlineLine = "场景：线上（你们隔着聊天软件发消息，不在一起）"
	sceneAtCanteen  = "场景：面对面，在食堂（"
)

// sceneSkill stands in for the built-in scene skill: private chats only,
// tool set_scene.
var sceneSkill = skills.Skill{Name: "scene", Description: "d", Tools: []string{"set_scene"},
	Conversations: []string{"private"}, Body: "- 用 set_scene。"}

// sceneEnv is stateEnv whose skills include sceneSkill, with set_scene a
// fake tool.
func sceneEnv(t *testing.T, botSkills []string) (*testEnv, *injectClock) {
	t.Helper()
	clock := newInjectClock(injectMorning)
	env := skillEnvWith(t, testOpts{now: clock.now, configure: func(c *config.Config) { c.Bots[0].PrivateChat = "all" }},
		[]skills.Skill{sceneSkill}, botSkills,
		&recTool{name: "set_scene"}, &recTool{name: "set_status"}, &recTool{name: "adjust_buff"}, &recTool{name: "add_buff"},
		&recTool{name: "remove_buff"}, &recTool{name: "now"})
	return env, clock
}

// setScene stores mode and place, from since and last touched at touched, as
// the scene of the private chat's memory scope.
func (e *testEnv) setScene(t *testing.T, mode, place string, since, touched time.Time) {
	t.Helper()
	_, err := e.st.SetScene(context.Background(), store.ConversationScene{ConversationKey: memory.ScopeKeyOf(privateConvKey),
		Mode: mode, Place: place, SinceAt: since, TouchedAt: touched, UpdatedAt: since})
	if err != nil {
		t.Fatal(err)
	}
}

// storedScene returns the stored scene of the memory scope of convKey.
func (e *testEnv) storedScene(t *testing.T, convKey string) (store.ConversationScene, bool) {
	t.Helper()
	sc, err := e.st.GetScene(context.Background(), memory.ScopeKeyOf(convKey))
	if errors.Is(err, store.ErrNotFound) {
		return store.ConversationScene{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return sc, true
}

func TestEffectiveScene_IdleBoundary(t *testing.T) {
	now := injectMorning
	inPerson := func(touched time.Time) store.ConversationScene {
		return store.ConversationScene{ConversationKey: "k", Mode: conloomapi.SceneInPerson, Place: "食堂",
			SinceAt: touched.Add(-time.Hour), TouchedAt: touched, PrevMode: conloomapi.SceneOnline}
	}

	touched := now.Add(-sceneIdleTimeout)
	eff, fellBack := effectiveScene(inPerson(touched), true, now)
	want := store.ConversationScene{ConversationKey: "k", Mode: conloomapi.SceneOnline, SinceAt: touched.Add(sceneIdleTimeout),
		TouchedAt: touched, PrevMode: conloomapi.SceneInPerson, PrevPlace: "食堂"}
	if !fellBack || eff != want {
		t.Errorf("touched 3h ago: effectiveScene() = %+v, %v; want %+v, true", eff, fellBack, want)
	}
	if !idleOnline(eff) {
		t.Errorf("idleOnline(%+v) = false, want true for a fallback", eff)
	}

	recent := inPerson(now.Add(-sceneIdleTimeout + time.Second))
	if eff, fellBack := effectiveScene(recent, true, now); fellBack || eff != recent {
		t.Errorf("touched under 3h ago: effectiveScene() = %+v, %v; want it unchanged", eff, fellBack)
	}

	if eff, fellBack := effectiveScene(store.ConversationScene{}, false, now); fellBack ||
		eff != (store.ConversationScene{Mode: conloomapi.SceneOnline}) {
		t.Errorf("no row: effectiveScene() = %+v, %v; want online", eff, fellBack)
	}

	online := store.ConversationScene{Mode: conloomapi.SceneOnline, SinceAt: now, TouchedAt: now,
		PrevMode: conloomapi.SceneInPerson, PrevPlace: "食堂"}
	if eff, fellBack := effectiveScene(online, true, now.Add(48*time.Hour)); fellBack || eff != online {
		t.Errorf("online: effectiveScene() = %+v, %v; want it unchanged", eff, fellBack)
	}
	if idleOnline(online) {
		t.Errorf("idleOnline(%+v) = true for a switch to online", online)
	}
	if idleOnline(inPerson(touched)) {
		t.Errorf("idleOnline() = true for an in-person scene")
	}
}

func TestState_SceneLine(t *testing.T) {
	t.Run("online without a row", func(t *testing.T) {
		env, clock := sceneEnv(t, []string{"scene"})
		want := autonomy.RenderState(&autonomy.StateScene{}, nil, nil, nil, clock.now(), env.o.loc)
		if !strings.Contains(want, sceneOnlineLine) {
			t.Fatalf("test block lacks the online line:\n%s", want)
		}
		if last := lastContent(env.statePrivateRequest(t, clock, "在吗")); !strings.HasPrefix(last, want+"\n\n【当前消息】") {
			t.Fatalf("last user message = %q, want it to start with\n%s", last, want)
		}
		if _, found := env.storedScene(t, privateConvKey); found {
			t.Errorf("reading the online default stored a scene")
		}
	})

	t.Run("in person", func(t *testing.T) {
		env, clock := sceneEnv(t, []string{"scene", "buff"})
		env.setScene(t, conloomapi.SceneInPerson, "食堂", clock.now().Add(-time.Hour), clock.now().Add(-time.Hour))
		last := lastContent(env.statePrivateRequest(t, clock, "在吗"))
		block, _, _ := strings.Cut(last, "\n\n【当前消息】")
		lines := strings.Split(block, "\n")
		if len(lines) < 3 || !strings.HasPrefix(lines[0], stateHeader) || !strings.HasPrefix(lines[1], sceneAtCanteen) ||
			!strings.HasPrefix(lines[2], "心情：") {
			t.Fatalf("state block =\n%s\nwant the in-person scene line first, then the bars", block)
		}
	})

	t.Run("bot without the skill", func(t *testing.T) {
		env, clock := sceneEnv(t, []string{"status"})
		env.setScene(t, conloomapi.SceneInPerson, "食堂", clock.now().Add(-time.Hour), clock.now().Add(-time.Hour))
		if text := requestText(t, env.statePrivateRequest(t, clock, "在吗")); strings.Contains(text, "场景：") ||
			strings.Contains(text, stateHeader) {
			t.Errorf("request shows a scene or a state block without the scene skill:\n%s", text)
		}
		if sc, _ := env.storedScene(t, privateConvKey); !sc.TouchedAt.Equal(clock.now().Add(-time.Hour)) {
			t.Errorf("scene touched without the scene skill: %+v", sc)
		}
	})
}

func TestState_SceneFallbackBeforeTouch(t *testing.T) {
	env, clock := sceneEnv(t, []string{"scene"})
	touched := clock.now().Add(-24 * time.Hour)
	env.setScene(t, conloomapi.SceneInPerson, "食堂", touched.Add(-time.Hour), touched)

	last := lastContent(env.statePrivateRequest(t, clock, "早上好"))
	if want := autonomy.RenderState(&autonomy.StateScene{}, nil, nil, nil, clock.now(), env.o.loc); !strings.HasPrefix(last, want+"\n\n") {
		t.Fatalf("last user message = %q, want it to start with the online scene\n%s", last, want)
	}
	sc, found := env.storedScene(t, privateConvKey)
	if !found || sc.Mode != conloomapi.SceneOnline || sc.Place != "" || sc.PrevMode != conloomapi.SceneInPerson ||
		sc.PrevPlace != "食堂" || !sc.SinceAt.Equal(touched.Add(sceneIdleTimeout)) || !sc.TouchedAt.Equal(touched) {
		t.Errorf("stored scene = %+v (found %v), want online since %v after 食堂, touched_at kept",
			sc, found, touched.Add(sceneIdleTimeout))
	}
	if !idleOnline(sc) {
		t.Errorf("idleOnline(%+v) = false for the written-back fallback", sc)
	}
}

func TestState_SceneTouchedByPersonOnly(t *testing.T) {
	t.Run("reply", func(t *testing.T) {
		env, clock := sceneEnv(t, []string{"scene"})
		env.setScene(t, conloomapi.SceneInPerson, "食堂", clock.now().Add(-time.Hour), clock.now().Add(-time.Hour))
		clock.set(clock.now().Add(time.Minute))
		if last := lastContent(env.statePrivateRequest(t, clock, "好吃吗")); !strings.Contains(last, sceneAtCanteen) {
			t.Fatalf("last user message = %q, want the in-person scene", last)
		}
		if sc, _ := env.storedScene(t, privateConvKey); sc.Mode != conloomapi.SceneInPerson || !sc.TouchedAt.Equal(clock.now()) {
			t.Errorf("stored scene = %+v, want in person touched at %v", sc, clock.now())
		}
	})

	t.Run("initiate wake", func(t *testing.T) {
		env, clock := newWakeEnvWith(t, nil, []skills.Skill{sceneSkill}, []string{"scene"}, nil)
		env.seedPrivateMessage(t, clock, "我在食堂等你")
		clock.set(clock.now().Add(5 * time.Hour))
		touched := clock.now().Add(-time.Hour)
		env.setScene(t, conloomapi.SceneInPerson, "食堂", touched, touched)

		task := env.fireWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("initiate task = %+v, want one reply", task)
		}
		if last := lastContent(wakeRequest(t, env)); !strings.Contains(last, sceneAtCanteen) {
			t.Errorf("wake request = %q, want the in-person scene", last)
		}
		if sc, _ := env.storedScene(t, privateConvKey); sc.Mode != conloomapi.SceneInPerson || !sc.TouchedAt.Equal(touched) {
			t.Errorf("stored scene = %+v, want in person still touched at %v", sc, touched)
		}
	})
}

func TestState_SceneSharedByPanelChats(t *testing.T) {
	env, clock := sceneEnv(t, []string{"scene"})
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	scope := memory.ScopeKeyOf(a)
	set := clock.now()
	if _, err := env.st.SetScene(context.Background(), store.ConversationScene{ConversationKey: scope, OriginConversationKey: a,
		Mode: conloomapi.SceneInPerson, Place: "图书馆", SinceAt: set, TouchedAt: set, UpdatedAt: set}); err != nil {
		t.Fatal(err)
	}
	clock.set(set.Add(time.Hour))

	if inB := env.panelReply(t, b, "在吗"); !strings.Contains(inB, "\n场景：面对面，在图书馆（") {
		t.Fatalf("B's request lacks the scene set in A:\n%s", inB)
	}
	if sc, _ := env.storedScene(t, b); sc.ConversationKey != scope || sc.Origin() != a || !sc.TouchedAt.Equal(clock.now()) {
		t.Errorf("scope scene = %+v, want A's touched by B's message at %v", sc, clock.now())
	}
}
