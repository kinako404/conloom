package core

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// sceneWakeEnv is newWakeEnvWith whose skills include sceneSkill and whose
// bot enables botSkills, with 小明's message seeded at the clock's time.
func sceneWakeEnv(t *testing.T, botSkills []string) (*testEnv, *injectClock) {
	t.Helper()
	env, clock := newWakeEnvWith(t, nil, []skills.Skill{sceneSkill}, botSkills, nil)
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	return env, clock
}

// errandTask is the task setWakeStatus stores as the one that set the status.
const errandTask = "task_seed"

// statusReturn stores 洗澡 from started for d as the private chat's status,
// fires its return at its end and returns the last user message of the wake
// task's request.
func (e *testEnv) statusReturn(t *testing.T, clock *injectClock, started time.Time, d time.Duration) string {
	t.Helper()
	return e.statusReturnAfter(t, clock, started, d, nil)
}

// statusReturnAfter is statusReturn that calls during, unless nil, halfway
// through the status, with the clock there.
func (e *testEnv) statusReturnAfter(t *testing.T, clock *injectClock, started time.Time, d time.Duration, during func()) string {
	t.Helper()
	until := started.Add(d)
	timer := e.setWakeStatus(t, "洗澡", started, until)
	if during != nil {
		clock.set(started.Add(d / 2))
		during()
	}
	clock.set(until)
	if task := e.fireWake(t, timer); task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("status return task = %+v, want one reply", task)
	}
	return lastContent(wakeRequest(t, e))
}

// requireScene fails unless the private chat's stored scene is mode at place,
// since since and touched at touched, after prevMode.
func requireScene(t *testing.T, env *testEnv, mode, place string, since, touched time.Time, prevMode string) store.ConversationScene {
	t.Helper()
	sc, found := env.storedScene(t, privateConvKey)
	if !found || sc.Mode != mode || sc.Place != place || !sc.SinceAt.Equal(since) || !sc.TouchedAt.Equal(touched) ||
		sc.PrevMode != prevMode {
		t.Fatalf("stored scene = %+v (found %v), want %s %q since %v touched %v after %s",
			sc, found, mode, place, since, touched, prevMode)
	}
	return sc
}

func TestSceneReturn_RestoresInPerson(t *testing.T) {
	env, clock := sceneWakeEnv(t, []string{"scene", "status", "buff"})
	t0 := clock.now()
	env.setScene(t, conloomapi.SceneInPerson, "食堂", t0, t0)
	left := t0.Add(time.Minute)
	env.setSceneBy(t, conloomapi.SceneOnline, "", left, left, errandTask)

	last := env.statusReturn(t, clock, left, 20*time.Minute)
	now := clock.now()
	sc := requireScene(t, env, conloomapi.SceneInPerson, "食堂", now, now, conloomapi.SceneOnline)
	if sc.PrevPlace != "" || sc.ConversationKey != memory.ScopeKeyOf(privateConvKey) || sc.Origin() != privateConvKey {
		t.Errorf("stored scene = %+v, want it restored by the private chat after online", sc)
	}
	if !strings.Contains(last, "你刚做完「洗澡」，回到了对方身边") || !strings.Contains(last, sceneAtCanteen) ||
		!strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", true)) {
		t.Fatalf("last user message = %q, want the in-person scene and the in-person status return", last)
	}
	if strings.Contains(last, sceneOnlineLine) {
		t.Errorf("last user message = %q, want no online scene line", last)
	}
}

func TestSceneReturn_OnlyTheTaskThatLeft(t *testing.T) {
	for _, c := range []struct {
		name     string
		task     string
		left     time.Duration
		restored bool
	}{
		{"switched just before the status", errandTask, -time.Second, true},
		{"switched after the status", errandTask, time.Second, true},
		{"switched long before by the same task", errandTask, -time.Hour, true},
		{"the person left in another task", "task_left", time.Minute, false},
		{"the person left before the errand", "task_left", -time.Minute, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, clock := sceneWakeEnv(t, []string{"scene", "status", "buff"})
			t0 := clock.now()
			started := t0.Add(2 * time.Hour)
			left := started.Add(c.left)
			env.setScene(t, conloomapi.SceneInPerson, "食堂", t0, t0)
			env.setSceneBy(t, conloomapi.SceneOnline, "", left, left, c.task)

			last := env.statusReturn(t, clock, started, 20*time.Minute)
			if c.restored {
				requireScene(t, env, conloomapi.SceneInPerson, "食堂", clock.now(), clock.now(), conloomapi.SceneOnline)
				if !strings.Contains(last, sceneAtCanteen) || !strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", true)) {
					t.Fatalf("last user message = %q, want the in-person scene and status return", last)
				}
				return
			}
			requireScene(t, env, conloomapi.SceneOnline, "", left, left, conloomapi.SceneInPerson)
			if !strings.Contains(last, sceneOnlineLine) || !strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", false)) {
				t.Fatalf("last user message = %q, want the online scene and status return", last)
			}
		})
	}
}

func TestSceneReturn_PanelOnlineDuringErrandStays(t *testing.T) {
	for _, source := range []string{store.BuffSourcePanel, store.BuffSourceAPI} {
		t.Run(source, func(t *testing.T) {
			env, clock := sceneWakeEnv(t, []string{"scene", "status", "buff"})
			t0 := clock.now()
			env.setScene(t, conloomapi.SceneInPerson, "食堂", t0, t0)
			var switched time.Time
			last := env.statusReturnAfter(t, clock, t0.Add(time.Minute), 20*time.Minute, func() {
				switched = clock.now()
				if err := env.o.Autonomy().SetScene(context.Background(), privateConvKey, conloomapi.SceneOnline, "", source, "operator"); err != nil {
					t.Fatal(err)
				}
			})
			requireScene(t, env, conloomapi.SceneOnline, "", switched, switched, conloomapi.SceneInPerson)
			if !strings.Contains(last, sceneOnlineLine) || !strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", false)) {
				t.Fatalf("last user message = %q, want the online scene and status return", last)
			}
		})
	}
}

func TestSceneReturn_NeverSwitchedOnline(t *testing.T) {
	env, clock := sceneWakeEnv(t, []string{"scene", "status", "buff"})
	t0 := clock.now()
	env.setScene(t, conloomapi.SceneInPerson, "食堂", t0, t0)
	before, _ := env.storedScene(t, privateConvKey)

	last := env.statusReturn(t, clock, t0.Add(10*time.Minute), 20*time.Minute)
	after := requireScene(t, env, conloomapi.SceneInPerson, "食堂", t0, t0, conloomapi.SceneOnline)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("stored scene = %+v, want %+v unchanged", after, before)
	}
	if !strings.Contains(last, sceneAtCanteen) || !strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", true)) {
		t.Fatalf("last user message = %q, want the in-person scene and status return", last)
	}
}

func TestSceneReturn_OnlineBeforeErrandStaysOnline(t *testing.T) {
	env, clock := sceneWakeEnv(t, []string{"scene", "status", "buff"})
	t0 := clock.now()
	env.setScene(t, conloomapi.SceneInPerson, "食堂", t0.Add(-3*time.Hour), t0.Add(-3*time.Hour))
	left := t0.Add(-2 * time.Hour)
	env.setScene(t, conloomapi.SceneOnline, "", left, left)

	last := env.statusReturn(t, clock, t0, 20*time.Minute)
	requireScene(t, env, conloomapi.SceneOnline, "", left, left, conloomapi.SceneInPerson)
	if !strings.Contains(last, sceneOnlineLine) || !strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", false)) {
		t.Fatalf("last user message = %q, want the online scene and status return", last)
	}
}

func TestSceneReturn_ErrandOutlastsSilence(t *testing.T) {
	for name, task := range map[string]string{"set in person by the panel": "", "set in person by the errand's task": errandTask} {
		t.Run(name, func(t *testing.T) {
			env, clock := sceneWakeEnv(t, []string{"scene", "status", "buff"})
			t0 := clock.now()
			env.setSceneBy(t, conloomapi.SceneInPerson, "食堂", t0, t0, task)

			last := env.statusReturn(t, clock, t0, 4*time.Hour)
			sc := requireScene(t, env, conloomapi.SceneOnline, "", t0.Add(sceneIdleTimeout), t0, conloomapi.SceneInPerson)
			if sc.SwitchTaskID != "" {
				t.Errorf("stored scene = %+v, want the fallback switched by no task", sc)
			}
			if !strings.Contains(last, sceneOnlineLine) || !strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", false)) {
				t.Fatalf("last user message = %q, want the online scene and status return", last)
			}
		})
	}
}

func TestSceneReturn_FallbackDuringErrandStays(t *testing.T) {
	env, clock := sceneWakeEnv(t, []string{"scene", "status", "buff"})
	t0 := clock.now()
	env.setSceneBy(t, conloomapi.SceneInPerson, "食堂", t0, t0, errandTask)
	fellBack := t0.Add(sceneIdleTimeout)

	last := env.statusReturnAfter(t, clock, t0.Add(2*time.Hour), 2*time.Hour, func() {
		if got, err := env.o.Autonomy().Scene(context.Background(), privateConvKey); err != nil || !got.IdleOnline {
			t.Fatalf("Scene halfway = %+v, %v; want idle online", got, err)
		}
		if ended, err := env.st.EndIdleScene(context.Background(), memory.ScopeKeyOf(privateConvKey), t0, fellBack); err != nil || !ended {
			t.Fatalf("EndIdleScene = %v, %v", ended, err)
		}
	})
	requireScene(t, env, conloomapi.SceneOnline, "", fellBack, t0, conloomapi.SceneInPerson)
	if !strings.Contains(last, sceneOnlineLine) || !strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", false)) {
		t.Fatalf("last user message = %q, want the online scene and status return", last)
	}
}

func TestSceneReturn_SkillOff(t *testing.T) {
	env, clock := sceneWakeEnv(t, []string{"status", "buff"})
	t0 := clock.now()
	env.setScene(t, conloomapi.SceneInPerson, "食堂", t0, t0)
	left := t0.Add(time.Minute)
	env.setScene(t, conloomapi.SceneOnline, "", left, left)

	last := env.statusReturn(t, clock, left, 20*time.Minute)
	requireScene(t, env, conloomapi.SceneOnline, "", left, left, conloomapi.SceneInPerson)
	if strings.Contains(last, "场景：") || !strings.HasSuffix(last, "\n"+CopyDirectiveStatusReturn("洗澡", false)) {
		t.Fatalf("last user message = %q, want no scene and the online status return", last)
	}
}

func TestWake_SceneDirectives(t *testing.T) {
	for _, inPerson := range []bool{true, false} {
		for _, kind := range []store.TimerKind{store.TimerInitiate, store.TimerFollowUp} {
			env, clock := newWakeEnvWith(t, nil, []skills.Skill{sceneSkill}, []string{"scene", "status", "buff"}, nil)
			env.seedPrivateMessage(t, clock, "我在食堂等你")
			clock.set(clock.now().Add(2 * time.Hour))
			if kind == store.TimerFollowUp {
				env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now())
				clock.set(clock.now().Add(30 * time.Minute))
			}
			if inPerson {
				touched := clock.now().Add(-time.Hour)
				env.setScene(t, conloomapi.SceneInPerson, "食堂", touched, touched)
			}

			if task := env.fireWake(t, env.armWake(t, kind, clock.now())); task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
				t.Fatalf("%s (in person %v) task = %+v, want one reply", kind, inPerson, task)
			}
			last := lastContent(wakeRequest(t, env))
			online := "主动找 小明 说话"
			if kind == store.TimerFollowUp {
				online = "\n" + CopyDirectiveFollowUp("小明", true, false)
			}
			switch {
			case inPerson && !strings.Contains(last, "你们现在在一起"):
				t.Errorf("%s in person: last user message = %q, want the in-person directive", kind, last)
			case !inPerson && (!strings.Contains(last, online) || strings.Contains(last, "你们现在在一起")):
				t.Errorf("%s online: last user message = %q, want the online directive", kind, last)
			}
		}
	}
}

func TestWake_SetSceneNotOffered(t *testing.T) {
	buffTools := []string{"adjust_buff", "add_buff", "remove_buff"}
	for _, kind := range []store.TimerKind{store.TimerInitiate, store.TimerStatusReturn} {
		env, clock := newWakeEnvWith(t, nil, []skills.Skill{sceneSkill}, []string{"scene", "status", "buff"}, nil)
		env.seedPrivateMessage(t, clock, "我去洗个澡")
		touched := clock.now()
		env.setScene(t, conloomapi.SceneInPerson, "食堂", touched, touched)
		var timer store.ConversationTimer
		if kind == store.TimerStatusReturn {
			until := clock.now().Add(20 * time.Minute)
			timer = env.setWakeStatus(t, "洗澡", clock.now(), until)
			clock.set(until)
		} else {
			clock.set(clock.now().Add(time.Hour))
			timer = env.armWake(t, kind, clock.now())
		}

		if task := env.fireWake(t, timer); len(task.Actions) != 1 {
			t.Fatalf("%s task = %+v, want a reply", kind, task)
		}
		req := wakeRequest(t, env)
		if got := offeredTools(req); !reflect.DeepEqual(got, buffTools) {
			t.Errorf("%s request tools = %v, want %v", kind, got, buffTools)
		}
		if sys := requestMessages(t, req)[0]["content"].(string); strings.Contains(sys, "set_scene") {
			t.Errorf("%s system prompt describes set_scene:\n%s", kind, sys)
		}
	}
}

func TestCopyDirective_SceneVariants(t *testing.T) {
	variants := map[string]string{
		"initiate":            CopyDirectiveInitiate("小明", "星期四 16:00", "约 2 小时", false, true),
		"initiate no gap":     CopyDirectiveInitiate("小明", "星期四 16:00", "", false, true),
		"initiate protected":  CopyDirectiveInitiate("小明", "星期四 16:00", "约 2 小时", true, true),
		"follow-up":           CopyDirectiveFollowUp("小明", false, true),
		"follow-up protected": CopyDirectiveFollowUp("小明", true, true),
		"status return":       CopyDirectiveStatusReturn("洗澡", true),
	}
	for name, s := range variants {
		if !strings.HasPrefix(s, "【提示】") || !strings.Contains(s, SkipMarker) || !strings.Contains(s, "你们现在在一起") ||
			!strings.Contains(s, "在对方身边开口") || strings.Contains(s, "发消息") {
			t.Errorf("%s in person = %q", name, s)
		}
		if protected := strings.HasSuffix(name, "protected"); strings.Contains(s, noRomanceLine) != protected ||
			protected && !strings.HasSuffix(s, noRomanceLine) {
			t.Errorf("%s in person = %q, want %q only at the end of a protected one", name, s, noRomanceLine)
		}
	}
	if s := variants["initiate"]; !strings.Contains(s, "小明") || !strings.Contains(s, "现在是 星期四 16:00，") ||
		!strings.Contains(s, "约 2 小时前") || !strings.Contains(s, "不要提“主动”“系统”“提示”") {
		t.Errorf("initiate in person = %q", s)
	}
	if s := variants["initiate no gap"]; strings.Contains(s, "上次") || !strings.Contains(s, "现在是 星期四 16:00。") {
		t.Errorf("initiate in person without a gap = %q", s)
	}
	if s := variants["follow-up"]; !strings.Contains(s, "小明") || !strings.Contains(s, "不要催促或抱怨") {
		t.Errorf("follow-up in person = %q", s)
	}
	if s := variants["status return"]; !strings.HasPrefix(s, "【提示】你刚做完「洗澡」，回到了对方身边，你们现在在一起。") {
		t.Errorf("status return in person = %q", s)
	}
}
