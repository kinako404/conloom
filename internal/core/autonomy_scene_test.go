package core

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// checkSceneAudits fails unless the autonomy.scene.set audits are the
// private chat's by actor with the details want, in any order: audits written
// within a millisecond have no order.
func (e *testEnv) checkSceneAudits(t *testing.T, actor string, want ...string) {
	t.Helper()
	audits := e.audits(t, "autonomy.scene.set")
	got := make([]string, len(audits))
	for i, a := range audits {
		if a.Actor != actor || a.Target != privateConvKey || a.BotInstanceID != testBot {
			t.Errorf("audit = %+v, want %s's of %s", a, actor, privateConvKey)
		}
		got[i] = a.Detail
	}
	slices.Sort(got)
	want = slices.Sorted(slices.Values(want))
	if !slices.Equal(got, want) {
		t.Errorf("autonomy.scene.set audit details = %q, want %q", got, want)
	}
}

func TestAutonomyService_Scene(t *testing.T) {
	ctx := context.Background()

	t.Run("bot without the skill", func(t *testing.T) {
		env, clock := sceneEnv(t, []string{"status"})
		env.seedPrivateMessage(t, clock, "在吗")
		got, err := env.o.Autonomy().Scene(ctx, privateConvKey)
		if err != nil {
			t.Fatal(err)
		}
		if got != (conloomapi.AutonomyScene{Mode: conloomapi.SceneOnline}) {
			t.Errorf("Scene = %+v, want online and not enabled", got)
		}
	})

	env, clock := sceneEnv(t, []string{"scene"})
	env.seedPrivateMessage(t, clock, "在吗")
	svc := env.o.Autonomy()
	scene := func() conloomapi.AutonomyScene {
		t.Helper()
		got, err := svc.Scene(ctx, privateConvKey)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := scene(); got != (conloomapi.AutonomyScene{Enabled: true, Mode: conloomapi.SceneOnline}) {
		t.Errorf("Scene without a row = %+v, want enabled and online", got)
	}

	now := clock.now()
	since := now.Add(-2 * time.Hour)
	env.setScene(t, conloomapi.SceneInPerson, "食堂", since, now.Add(-time.Hour))
	if got := scene(); got != (conloomapi.AutonomyScene{Enabled: true, Mode: conloomapi.SceneInPerson, Place: "食堂", SinceAt: since.UTC()}) {
		t.Errorf("Scene in person = %+v, want 食堂 since %v", got, since)
	}

	touched := now.Add(-sceneIdleTimeout)
	env.setScene(t, conloomapi.SceneInPerson, "食堂", since, touched)
	want := conloomapi.AutonomyScene{Enabled: true, Mode: conloomapi.SceneOnline, SinceAt: now.UTC(), IdleOnline: true}
	if got := scene(); got != want {
		t.Errorf("Scene after 3h of silence = %+v, want %+v", got, want)
	}
	if sc, _ := env.storedScene(t, privateConvKey); sc.Mode != conloomapi.SceneInPerson || !sc.TouchedAt.Equal(touched) {
		t.Errorf("stored scene = %+v, want it unchanged by the read", sc)
	}

	if ended, err := env.st.EndIdleScene(ctx, privateConvKey, touched, now); err != nil || !ended {
		t.Fatalf("EndIdleScene = %v, %v", ended, err)
	}
	clock.set(now.Add(time.Hour))
	if got := scene(); got != want {
		t.Errorf("Scene of the stored fallback = %+v, want %+v", got, want)
	}

	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneInPerson, "图书馆", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneOnline, "", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if got := scene(); got != (conloomapi.AutonomyScene{Enabled: true, Mode: conloomapi.SceneOnline, SinceAt: clock.now().UTC()}) {
		t.Errorf("Scene switched online by the panel = %+v, want online, not idle", got)
	}
}

func TestAutonomyService_SetScene(t *testing.T) {
	env, clock := sceneEnv(t, []string{"scene"})
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "在吗")
	now := clock.now()

	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneInPerson, "食堂", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	sc, found := env.storedScene(t, privateConvKey)
	if !found || sc.ConversationKey != privateConvKey || sc.Origin() != privateConvKey || sc.Mode != conloomapi.SceneInPerson ||
		sc.Place != "食堂" || sc.PrevMode != conloomapi.SceneOnline || sc.PrevPlace != "" || !sc.SinceAt.Equal(now) ||
		!sc.TouchedAt.Equal(now) {
		t.Fatalf("stored scene = %+v (found %v), want in person at 食堂 since and touched %v", sc, found, now)
	}
	set := `{"mode":"in_person","place":"食堂","source":"panel","changed":true}`
	env.checkSceneAudits(t, "panel:admin", set)

	clock.set(now.Add(time.Minute))
	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneInPerson, "食堂", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := env.storedScene(t, privateConvKey); !sc.SinceAt.Equal(now) || !sc.TouchedAt.Equal(clock.now()) {
		t.Errorf("stored scene after the same again = %+v, want since kept at %v, touched at %v", sc, now, clock.now())
	}
	same := `{"mode":"in_person","place":"食堂","source":"panel","changed":false}`
	env.checkSceneAudits(t, "panel:admin", set, same)

	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneOnline, "食堂", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := env.storedScene(t, privateConvKey); sc.Mode != conloomapi.SceneOnline || sc.Place != "" ||
		sc.PrevMode != conloomapi.SceneInPerson || sc.PrevPlace != "食堂" {
		t.Errorf("stored scene after online = %+v, want online without a place", sc)
	}
	env.checkSceneAudits(t, "panel:admin", set, same, `{"mode":"online","place":"","source":"panel","changed":true}`)

	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneOnline, "", store.BuffSourceModel, "panel:admin"); err == nil ||
		errors.Is(err, ErrInvalidScene) {
		t.Errorf("SetScene as the model err = %v, want an internal error", err)
	}
	env.seedGroupConv(t, clock.now())
	if err := svc.SetScene(ctx, testConvKey, conloomapi.SceneOnline, "", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("SetScene of a group err = %v, want ErrNotPrivate", err)
	}
	missing := store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "private_29999")
	if err := svc.SetScene(ctx, missing, conloomapi.SceneOnline, "", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetScene of an unknown chat err = %v, want ErrNotFound", err)
	}
	if n := len(env.audits(t, "autonomy.scene.set")); n != 3 {
		t.Errorf("refused changes wrote audits: %d in all, want 3", n)
	}
}

func TestAutonomyService_SetScenePlace(t *testing.T) {
	env, clock := sceneEnv(t, []string{"scene"})
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "在吗")

	err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneInPerson, strings.Repeat("食", conloomapi.ScenePlaceMaxRunes+1),
		store.BuffSourceAPI, "memory-main")
	if !errors.Is(err, ErrInvalidScene) || !strings.Contains(err.Error(), "16") {
		t.Errorf("SetScene with 17 runes err = %v, want ErrInvalidScene naming the limit", err)
	}
	err = svc.SetScene(ctx, privateConvKey, "offline", "", store.BuffSourceAPI, "memory-main")
	if !errors.Is(err, ErrInvalidScene) || !strings.Contains(err.Error(), "offline") {
		t.Errorf("SetScene offline err = %v, want ErrInvalidScene naming the mode", err)
	}
	if sc, found := env.storedScene(t, privateConvKey); found {
		t.Fatalf("refused changes stored %+v", sc)
	}
	if n := len(env.audits(t, "autonomy.scene.set")); n != 0 {
		t.Fatalf("refused changes wrote %d audits", n)
	}

	for in, want := range map[string]string{
		"【图书馆】":                             "图书馆",
		"食\n堂":                              "食 堂",
		strings.Repeat("食", 16):             strings.Repeat("食", 16),
		" " + strings.Repeat("食", 16) + " ": strings.Repeat("食", 16),
	} {
		if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneInPerson, in, store.BuffSourceAPI, "memory-main"); err != nil {
			t.Errorf("SetScene(%q): %v", in, err)
			continue
		}
		if sc, _ := env.storedScene(t, privateConvKey); sc.Place != want {
			t.Errorf("SetScene(%q) stored place %q, want %q", in, sc.Place, want)
		}
	}
}

func TestAutonomyService_SetSceneEmptyPlace(t *testing.T) {
	env, clock := sceneEnv(t, []string{"scene"})
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "我到了")

	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneInPerson, "  ", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if sc, found := env.storedScene(t, privateConvKey); !found || sc.Mode != conloomapi.SceneInPerson || sc.Place != "" {
		t.Fatalf("stored scene = %+v (found %v), want in person without a place", sc, found)
	}
	st, err := svc.State(ctx, privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if want := (conloomapi.AutonomyScene{Enabled: true, Mode: conloomapi.SceneInPerson, SinceAt: clock.now().UTC()}); st.Scene != want {
		t.Errorf("State().Scene = %+v, want %+v", st.Scene, want)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"scene":{"enabled":true,"mode":"in_person","place":"",`) {
		t.Errorf("State JSON = %s, want the scene", raw)
	}

	clock.set(clock.now().Add(time.Minute))
	block, _, _ := strings.Cut(lastContent(env.statePrivateRequest(t, clock, "好呀")), "\n\n【当前消息】")
	if !strings.Contains(block, "\n场景：面对面（你们在同一个地方；") || strings.Contains(block, "，在") {
		t.Errorf("state block =\n%s\nwant the in-person line without a place", block)
	}
}

func TestAutonomyService_SetSceneAfterSilence(t *testing.T) {
	env, clock := sceneEnv(t, []string{"scene"})
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "在吗")
	now := clock.now()
	touched := now.Add(-4 * time.Hour)
	env.setScene(t, conloomapi.SceneInPerson, "食堂", touched.Add(-time.Hour), touched)

	// Online already, in effect: nothing changes.
	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneOnline, "", store.BuffSourceAPI, "memory-main"); err != nil {
		t.Fatal(err)
	}
	sc, _ := env.storedScene(t, privateConvKey)
	if sc.Mode != conloomapi.SceneOnline || sc.PrevMode != conloomapi.SceneInPerson || sc.PrevPlace != "食堂" ||
		!sc.SinceAt.Equal(touched.Add(sceneIdleTimeout)) {
		t.Errorf("stored scene = %+v, want the fallback since %v", sc, touched.Add(sceneIdleTimeout))
	}

	// Back in person at the same place: a new scene from now.
	if err := svc.SetScene(ctx, privateConvKey, conloomapi.SceneInPerson, "食堂", store.BuffSourceAPI, "memory-main"); err != nil {
		t.Fatal(err)
	}
	sc, _ = env.storedScene(t, privateConvKey)
	if sc.Mode != conloomapi.SceneInPerson || sc.Place != "食堂" || sc.PrevMode != conloomapi.SceneOnline || !sc.SinceAt.Equal(now) ||
		!sc.TouchedAt.Equal(now) {
		t.Errorf("stored scene = %+v, want in person at 食堂 since %v", sc, now)
	}
	env.checkSceneAudits(t, "memory-main", `{"mode":"online","place":"","source":"api","changed":false}`,
		`{"mode":"in_person","place":"食堂","source":"api","changed":true}`)
}
