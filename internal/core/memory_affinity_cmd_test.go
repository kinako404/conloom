package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
)

func affinityOf(t *testing.T, env *testEnv, convKey, userID string) *int {
	t.Helper()
	return env.memberIn(t, convKey, userID).Affinity
}

func affinityRevisions(t *testing.T, env *testEnv, convKey, userID string) []store.MemoryRevision {
	t.Helper()
	revs, err := env.st.ListMemoryRevisions(context.Background(), convKey, userID)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.MemoryRevision
	for _, r := range revs {
		if r.Field == store.FieldAffinity {
			out = append(out, r)
		}
	}
	return out
}

func TestMemoryCommand_AffinityAdminSet(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	memCmdSeed(t, env, "29999", "喜欢咖啡", memCmdAffinity(52))
	for _, c := range []struct {
		text, want string
		value      int
	}{
		{"/好感度 设 79", "好感度已调整：织语对你的好感 亲近（79/100）", 79},
		{"/affinity set 100", "好感度已调整：织语对你的好感 很要好（100/100）", 100},
		{"/affinity SET 0", "好感度已调整：织语对你的好感 陌生（0/100）", 0},
		{"/好感度 set 35", "好感度已调整：织语对你的好感 认识（35/100）", 35},
	} {
		if got := env.command(t, "29999", c.text); got != c.want {
			t.Fatalf("%s = %q, want %q", c.text, got, c.want)
		}
		if v := affinityOf(t, env, testConvKey, "29999"); v == nil || *v != c.value {
			t.Fatalf("after %s affinity = %v, want %d", c.text, v, c.value)
		}
	}
	if m := env.memberIn(t, testConvKey, "29999"); m.Note != "喜欢咖啡" {
		t.Errorf("note changed: %+v", m)
	}
	if got, want := env.command(t, "29999", "/好感度"), "织语对你的好感：认识（35/100）"; got != want {
		t.Errorf("/好感度 after set = %q, want %q", got, want)
	}

	revs := affinityRevisions(t, env, testConvKey, "29999")
	if len(revs) != 4 {
		t.Fatalf("affinity revisions = %+v, want 4", revs)
	}
	for _, r := range revs {
		if r.Source != store.RevisionCommand || r.Reason != memory.ReasonAffinityCommand {
			t.Errorf("revision = %+v, want source %q reason %q", r, store.RevisionCommand, memory.ReasonAffinityCommand)
		}
	}
	if n := memCmdCounter(t, env, store.RevisionCommand); n != 4 {
		t.Errorf("affinity changes{command} = %v, want 4", n)
	}
	audits, err := env.st.ListAudit(context.Background(), store.AuditFilter{Kind: "memory.update", BotInstanceID: testBot, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 4 {
		t.Fatalf("memory.update audits = %+v, want 4", audits)
	}
	for _, a := range audits {
		var detail struct {
			UserID string   `json:"user_id"`
			Fields []string `json:"fields"`
		}
		if err := json.Unmarshal([]byte(a.Detail), &detail); err != nil {
			t.Fatal(err)
		}
		if a.Actor != "command:29999" || a.Target != testConvKey || detail.UserID != "29999" ||
			len(detail.Fields) != 1 || detail.Fields[0] != store.FieldAffinity {
			t.Errorf("audit = %+v (detail %+v)", a, detail)
		}
	}
}

func TestMemoryCommand_AffinityAdminDelta(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	for _, c := range []struct {
		text      string
		value     int
		unchanged bool
	}{
		{"/好感度 +5", memory.InitialAffinity + 5, false},
		{"/affinity +10", memory.InitialAffinity + 15, false},
		{"/好感度 -3", memory.InitialAffinity + 12, false},
		{"/好感度 +0", memory.InitialAffinity + 12, true},
		{"/好感度 -0", memory.InitialAffinity + 12, true},
		{"/好感度 设 32", memory.InitialAffinity + 12, true},
		{"/好感度 +9223372036854775807", 100, false},
		{"/好感度 -9223372036854775807", 0, false},
		{"/好感度 +200", 100, false},
		{"/affinity +1", 100, true},
		{"/好感度 -500", 0, false},
		{"/好感度 -5", 0, true},
		{"/affinity -9223372036854775808", 0, true},
	} {
		want := CopyAffinityAdjusted("织语", memory.TierName(c.value), c.value)
		if c.unchanged {
			want = CopyAffinityUnchanged("织语", memory.TierName(c.value), c.value)
		}
		if got := env.command(t, "29999", c.text); got != want {
			t.Fatalf("%s = %q, want %q", c.text, got, want)
		}
		if v := affinityOf(t, env, testConvKey, "29999"); v == nil || *v != c.value {
			t.Fatalf("after %s affinity = %v, want %d", c.text, v, c.value)
		}
	}
	if revs := affinityRevisions(t, env, testConvKey, "29999"); len(revs) != 7 {
		t.Errorf("affinity revisions = %+v, want 7 (unchanged values write nothing)", revs)
	}
	audits, err := env.st.ListAudit(context.Background(), store.AuditFilter{Kind: "memory.update", BotInstanceID: testBot, Limit: 20})
	if err != nil || len(audits) != 7 {
		t.Errorf("memory.update audits = %d, %v; want 7", len(audits), err)
	}
	if got, want := CopyAffinityUnchanged("织语", "认识", 32), "好感度没有变化：织语对你的好感 认识（32/100）"; got != want {
		t.Errorf("CopyAffinityUnchanged = %q, want %q", got, want)
	}
	if got, want := CopyAffinityAdjusted("织语", "认识", 25), "好感度已调整：织语对你的好感 认识（25/100）"; got != want {
		t.Errorf("CopyAffinityAdjusted = %q, want %q", got, want)
	}
}

func TestMemoryCommand_AffinityChangeAdminOnly(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		memCmdConfig(true, true)(c)
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 100
	}})
	memCmdSeed(t, env, "20001", "喜欢咖啡", memCmdAffinity(52))
	for _, text := range []string{"/好感度 设 80", "/好感度 +5", "/affinity -5", "/affinity set 100", "/好感度 设 abc"} {
		if got := env.command(t, "20001", text); got != CopyAdminOnly {
			t.Errorf("non-admin %s = %q, want %q", text, got, CopyAdminOnly)
		}
	}
	if v := affinityOf(t, env, testConvKey, "20001"); v == nil || *v != 52 {
		t.Errorf("non-admin changed affinity to %v", v)
	}
	if got, want := env.command(t, "20001", "/好感度"), "织语对你的好感：熟悉（52/100）"; got != want {
		t.Errorf("non-admin /好感度 = %q, want %q", got, want)
	}
	if got := env.command(t, "20001", "/好感度 foo"); got != CopyAffinityUsage {
		t.Errorf("non-admin /好感度 foo = %q, want usage", got)
	}
}

func TestMemoryCommand_AffinityInvalidInput(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	memCmdSeed(t, env, "29999", "喜欢咖啡", memCmdAffinity(52))
	for _, text := range []string{
		"/好感度 设 101", "/好感度 设 -1", "/好感度 设 abc", "/好感度 设", "/affinity set 1.5",
		"/好感度 +", "/好感度 -", "/好感度 +abc", "/好感度 +5 +5", "/好感度 设 1 2",
		"/好感度 foo", "/好感度 5", "/affinity +99999999999999999999",
	} {
		if got := env.command(t, "29999", text); got != CopyAffinityUsage {
			t.Errorf("%s = %q, want usage", text, got)
		}
	}
	if v := affinityOf(t, env, testConvKey, "29999"); v == nil || *v != 52 {
		t.Errorf("invalid input changed affinity to %v", v)
	}
	if revs := affinityRevisions(t, env, testConvKey, "29999"); len(revs) != 0 {
		t.Errorf("invalid input wrote revisions %+v", revs)
	}
}

func TestMemoryCommand_AffinityChangeNeedsMemoryAndAffinity(t *testing.T) {
	for name, c := range map[string]struct {
		enabled, affinity bool
		want              string
	}{
		"memory off":   {false, true, CopyMemoryDisabled},
		"affinity off": {true, false, CopyAffinityOff},
	} {
		t.Run(name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(c.enabled, c.affinity)})
			for _, text := range []string{"/好感度 设 80", "/affinity +5"} {
				if got := env.command(t, "29999", text); got != c.want {
					t.Errorf("%s = %q, want %q", text, got, c.want)
				}
			}
			if v := affinityOf(t, env, testConvKey, "29999"); v != nil {
				t.Errorf("affinity = %d, want none", *v)
			}
		})
	}
}

func TestMemoryCommand_AffinityChangePrivateChat(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		privMemConfig(true)(c)
		c.Bots[0].Admins = append(c.Bots[0].Admins, "20001")
	}})
	key := privKeyOf("20001")
	if got, want := env.privateCommand(t, "20001", "/好感度 设 60"), "好感度已调整：织语对你的好感 亲近（60/100）"; got != want {
		t.Fatalf("private /好感度 设 60 = %q, want %q", got, want)
	}
	if got, want := env.privateCommand(t, "20001", "/affinity -25"), "好感度已调整：织语对你的好感 认识（35/100）"; got != want {
		t.Fatalf("private /affinity -25 = %q, want %q", got, want)
	}
	if v := affinityOf(t, env, key, "20001"); v == nil || *v != 35 {
		t.Fatalf("private affinity = %v, want 35", v)
	}
	if v := affinityOf(t, env, testConvKey, "20001"); v != nil {
		t.Errorf("group affinity = %d, want none", *v)
	}
	revs := affinityRevisions(t, env, key, "20001")
	if len(revs) != 2 || revs[0].Source != store.RevisionCommand {
		t.Errorf("private revisions = %+v", revs)
	}
	audits, err := env.st.ListAudit(context.Background(), store.AuditFilter{Kind: "memory.update", BotInstanceID: testBot, Limit: 10})
	if err != nil || len(audits) != 2 || audits[0].Target != key || audits[0].Actor != "command:20001" {
		t.Errorf("private audits = %+v, %v", audits, err)
	}
}
