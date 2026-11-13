package core

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type memCmdTrigger struct {
	mu   sync.Mutex
	keys []string
}

func (f *memCmdTrigger) Trigger(convKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, convKey)
}

func (f *memCmdTrigger) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.keys)
}

func memCmdConfig(enabled, affinity bool) func(*config.Config) {
	return func(c *config.Config) {
		c.DefaultGroupPolicy.Memory.Enabled = enabled
		c.DefaultGroupPolicy.Memory.Affinity = affinity
	}
}

func memCmdAffinity(v int) *int { return &v }

func memCmdSeed(t *testing.T, env *testEnv, userID, note string, affinity *int) {
	t.Helper()
	conflicts, err := env.st.ApplyMemoryWrite(context.Background(), store.MemoryWrite{
		ConversationKey: testConvKey,
		Members: []store.MemberMemory{{ConversationKey: testConvKey, PlatformUserID: userID, DisplayName: "小明",
			Note: note, Affinity: affinity, LastInteractionDay: "2026-11-11"}},
		Revisions: []store.MemoryRevision{{ConversationKey: testConvKey, PlatformUserID: userID,
			Field: store.FieldNote, NewValue: note, Source: store.RevisionConsolidate}},
	})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("seed memory of %s: conflicts %v, err %v", userID, conflicts, err)
	}
}

func memCmdCounter(t *testing.T, env *testEnv, source string) float64 {
	t.Helper()
	mfs, err := env.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "conloom_memory_affinity_changes_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "source" && l.GetValue() == source {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestMemoryCommand_AffinityShowsTierAndValue(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	memCmdSeed(t, env, "20001", "喜欢咖啡", memCmdAffinity(52))
	for _, text := range []string{"/好感度", "/affinity"} {
		if got, want := env.command(t, "20001", text), "织语对你的好感：熟悉（52/100）"; got != want {
			t.Fatalf("%s = %q, want %q", text, got, want)
		}
	}

	p := conloomapi.DefaultPersona()
	p.CharacterName = "铃兰"
	bot, _ := env.cfg.Bot(testBot)
	if _, err := env.o.personas.Update(context.Background(), testConvKey, bot.PersonaTemplate,
		conloomapi.PersonaPut{Persona: &p, Reason: "test"}, "test"); err != nil {
		t.Fatal(err)
	}
	if got, want := env.command(t, "20001", "/好感度"), "铃兰对你的好感：熟悉（52/100）"; got != want {
		t.Fatalf("/好感度 with character name = %q, want %q", got, want)
	}
}

func TestMemoryCommand_AffinityNone(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	memCmdSeed(t, env, "20002", "喜欢猫", nil)
	for _, user := range []string{"20001", "20002"} {
		if got, want := env.command(t, user, "/好感度"), "织语和你还不熟。"; got != want {
			t.Fatalf("%s /好感度 = %q, want %q", user, got, want)
		}
	}
}

func TestMemoryCommand_DisabledReplies(t *testing.T) {
	for name, configure := range map[string]func(*config.Config){
		"memory off": memCmdConfig(false, true),
		"long requests off": func(c *config.Config) {
			memCmdConfig(true, true)(c)
			c.DefaultGroupPolicy.Concurrency.AllowLongRequests = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: configure})
			trig := &memCmdTrigger{}
			env.o.memTrigger = trig
			memCmdSeed(t, env, "20001", "喜欢咖啡", memCmdAffinity(52))
			for _, c := range []struct{ user, text string }{
				{"20001", "/好感度"}, {"20001", "/记忆"}, {"20001", "/memory"},
				{"29999", "/记忆 整理"}, {"29999", "/记忆 群"}, {"20001", "/记忆 整理"},
			} {
				if got := env.command(t, c.user, c.text); got != CopyMemoryDisabled {
					t.Errorf("%s %s = %q, want %q", c.user, c.text, got, CopyMemoryDisabled)
				}
			}
			if calls := trig.calls(); len(calls) != 0 {
				t.Errorf("trigger calls = %v, want none", calls)
			}
		})
	}
}

func TestMemoryCommand_AffinityOff(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, false)})
	memCmdSeed(t, env, "20001", "喜欢咖啡", memCmdAffinity(52))
	if got := env.command(t, "20001", "/好感度"); got != CopyAffinityOff {
		t.Fatalf("/好感度 = %q, want %q", got, CopyAffinityOff)
	}
	if got, want := env.command(t, "20001", "/记忆"), "我记得关于你的：喜欢咖啡"; got != want {
		t.Fatalf("/记忆 = %q, want %q", got, want)
	}
}

func TestMemoryCommand_NoteAndUsage(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	memCmdSeed(t, env, "20001", "喜欢咖啡", memCmdAffinity(52))
	if got, want := env.command(t, "20001", "/记忆"), "我记得关于你的：喜欢咖啡"; got != want {
		t.Fatalf("/记忆 = %q, want %q", got, want)
	}
	if got, want := env.command(t, "20002", "/memory"), "我还没有记下关于你的事。"; got != want {
		t.Fatalf("/memory without a note = %q, want %q", got, want)
	}
	for _, c := range []struct{ user, text string }{{"20001", "/记忆 别的"}, {"29999", "/记忆 整理 现在"}, {"29999", "/memory foo"}} {
		if got := env.command(t, c.user, c.text); got != CopyMemoryUsage {
			t.Errorf("%s %s = %q, want usage", c.user, c.text, got)
		}
	}
}

func TestMemoryCommand_ForgetMeWorksWhenDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(enabled, true)})
		memCmdSeed(t, env, "20001", "喜欢咖啡", memCmdAffinity(52))
		memCmdSeed(t, env, "20002", "喜欢猫", memCmdAffinity(30))
		before := time.Now()
		for _, c := range []struct{ user, text string }{{"20001", "/忘记我"}, {"20003", "/forgetme"}} {
			if got, want := env.command(t, c.user, c.text), "好的，已清空关于你的笔记和好感度。群聊摘要是大家共同的记录，不会逐人删除，会在之后的整理中慢慢被覆盖。"; got != want {
				t.Fatalf("enabled=%v %s %s = %q, want %q", enabled, c.user, c.text, got, want)
			}
		}
		got, err := env.st.GetMemberMemories(context.Background(), testConvKey, []string{"20001", "20002", "20003"})
		if err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"20001", "20003"} {
			m, ok := got[user]
			if !ok || m.Note != "" || m.Affinity != nil || m.ForgottenAt.Before(before.Add(-time.Second)) {
				t.Errorf("enabled=%v forgotten member %s = %+v (found %v)", enabled, user, m, ok)
			}
			revs, err := env.st.ListMemoryRevisions(context.Background(), testConvKey, user)
			if err != nil || len(revs) != 0 {
				t.Errorf("enabled=%v revisions of %s = %+v, %v; want none", enabled, user, revs, err)
			}
		}
		if m := got["20002"]; m.Note != "喜欢猫" || m.Affinity == nil || *m.Affinity != 30 {
			t.Errorf("enabled=%v other member changed: %+v", enabled, m)
		}
		if n := memCmdCounter(t, env, store.RevisionCommand); n != 1 {
			t.Errorf("enabled=%v affinity changes{command} = %v, want 1 (only 20001 had an affinity)", enabled, n)
		}
	}
}

func TestMemoryCommand_AdminSubcommands(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	trig := &memCmdTrigger{}
	env.o.memTrigger = trig

	for _, text := range []string{"/记忆 整理", "/memory consolidate", "/记忆 群", "/memory group"} {
		if got := env.command(t, "20001", text); got != CopyAdminOnly {
			t.Errorf("non-admin %s = %q, want %q", text, got, CopyAdminOnly)
		}
	}
	if calls := trig.calls(); len(calls) != 0 {
		t.Fatalf("non-admin consolidation triggered %v", calls)
	}

	for i, text := range []string{"/记忆 整理", "/memory CONSOLIDATE"} {
		if got, want := env.command(t, "29999", text), "已开始整理记忆，完成后不另外通知。"; got != want {
			t.Fatalf("admin %s = %q, want %q", text, got, want)
		}
		if calls := trig.calls(); len(calls) != i+1 || calls[i] != testConvKey {
			t.Fatalf("trigger calls after %s = %v, want %d× %s", text, calls, i+1, testConvKey)
		}
	}

	if got, want := env.command(t, "29999", "/记忆 群"), "还没有群聊摘要。"; got != want {
		t.Fatalf("/记忆 群 without summary = %q, want %q", got, want)
	}
	if err := env.st.PutGroupMemory(context.Background(), store.GroupMemory{ConversationKey: testConvKey}); err != nil {
		t.Fatal(err)
	}
	if got := env.command(t, "29999", "/记忆 群"); got != CopyMemoryNoSummary {
		t.Fatalf("/记忆 群 with an empty summary = %q", got)
	}
	if err := env.st.PutGroupMemory(context.Background(), store.GroupMemory{ConversationKey: testConvKey,
		Summary: "大家在聊咖啡。", CursorSeq: 3}); err != nil {
		t.Fatal(err)
	}
	if got, want := env.command(t, "29999", "/memory group"), "群聊摘要：大家在聊咖啡。"; got != want {
		t.Fatalf("/memory group = %q, want %q", got, want)
	}
}

func TestMemoryCommand_ConsolidateWhilePaused(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	trig := &memCmdTrigger{}
	env.o.memTrigger = trig
	if got := env.command(t, "29999", "/pause"); got != CopyPaused {
		t.Fatalf("/pause = %q", got)
	}
	for _, text := range []string{"/记忆 整理", "/memory consolidate"} {
		if got := env.command(t, "29999", text); got != CopyMemoryPaused {
			t.Fatalf("%s while paused = %q, want %q", text, got, CopyMemoryPaused)
		}
	}
	if calls := trig.calls(); len(calls) != 0 {
		t.Fatalf("paused consolidation triggered %v", calls)
	}
	if got := env.command(t, "29999", "/resume"); got != CopyResumed {
		t.Fatalf("/resume = %q", got)
	}
	if got := env.command(t, "29999", "/记忆 整理"); got != CopyMemoryConsolidating {
		t.Fatalf("/记忆 整理 after resuming = %q", got)
	}
	if calls := trig.calls(); len(calls) != 1 {
		t.Fatalf("trigger calls after resuming = %v, want 1", calls)
	}
}

func TestMemoryCommand_NilTriggerStillReplies(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	if got := env.command(t, "29999", "/记忆 整理"); got != CopyMemoryConsolidating {
		t.Fatalf("/记忆 整理 without a trigger = %q", got)
	}
}

func TestDecide_MemoryCommandsKnown(t *testing.T) {
	for _, word := range []string{"好感度", "affinity", "记忆", "memory", "忘记我", "forgetme"} {
		in := DecideInput{
			BotInstanceID: testBot, SelfPlatformID: "30001", ConversationType: conloomapi.ConvGroup,
			PlatformUserID: "20001", Text: "/" + word + " 群", CommandPrefix: "/", Enabled: true,
		}
		want := Decision{Disposition: conloomapi.DispositionReply, Trigger: conloomapi.TriggerCommand, Command: word, Args: "群"}
		if got := Decide(in); got != want {
			t.Errorf("Decide(/%s) = %+v, want %+v", word, got, want)
		}
	}
	if cmd, _, ok := ParseCommand("/AFFINITY", "/"); !ok || cmd != "affinity" {
		t.Errorf("ParseCommand(/AFFINITY) = %q, %v", cmd, ok)
	}
}

func TestCopyHelp_ListsMemoryCommands(t *testing.T) {
	help := CopyHelp("织语")
	for _, want := range []string{
		"，/好感度 查看好感度，/记忆 查看我记得的你，/记住 <内容> 让我记住一件事，/忘记我 清空你的记忆；管理员：",
		"，/记忆 整理 立即整理记忆，/记忆 群 查看群聊摘要。",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q: %q", want, help)
		}
	}
	for got, want := range map[string]string{
		CopyMemoryDisabled:        "这个群没有开启记忆。",
		CopyAffinityOff:           "这个群没有开启好感度。",
		CopyMemoryDisabledPrivate: "我们的私聊没有开启记忆。",
		CopyRemembered:            "记住了。",
	} {
		if got != want {
			t.Errorf("copy = %q, want %q", got, want)
		}
	}
	if got, want := CopyAffinity("织语", "认识", 21), "织语对你的好感：认识（21/100）"; got != want {
		t.Errorf("CopyAffinity = %q, want %q", got, want)
	}
	if CopyMemoryUsage != "用法：/记忆 查看我记得的关于你的事；/记住 <内容> 让我记住一件事；/好感度 查看好感度；/忘记我 清空你的记忆；管理员：/记忆 整理，/记忆 群（或 /记忆 摘要）。" {
		t.Errorf("CopyMemoryUsage = %q", CopyMemoryUsage)
	}
}
