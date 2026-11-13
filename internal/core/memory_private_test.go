package core

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func privKeyOf(user string) string {
	return store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "private_"+user)
}

// privMemConfig lets everyone chat privately and sets memory on or off with
// affinity on, for groups and private chats alike.
func privMemConfig(enabled bool) func(*config.Config) {
	return func(c *config.Config) {
		injectConfig(true)(c)
		c.DefaultGroupPolicy.Memory.Enabled = enabled
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 100
		c.Bots[0].PrivateChat = "all"
	}
}

// privateCommand sends text from user in their private chat and returns the
// text of the command's single reply.
func (e *testEnv) privateCommand(t *testing.T, user, text string) string {
	t.Helper()
	resp := e.submit(t, e.event(conloomapi.ConvPrivate, user, user, text, withName("小明")))
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("private command %q: response %+v", text, resp)
	}
	task := e.getTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("private command %q: task %+v", text, task)
	}
	return task.Actions[0].Content.Text
}

func (e *testEnv) memberIn(t *testing.T, convKey, userID string) store.MemberMemory {
	t.Helper()
	got, err := e.st.GetMemberMemories(context.Background(), convKey, []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	return got[userID]
}

func TestMemoryInject_PrivateChatBlock(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	key := privKeyOf("20001")
	env.injectSeed(t, key, "私聊里聊过猫",
		store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(52)},
		store.MemberMemory{PlatformUserID: "20002", Note: "别人的笔记"})
	ev := env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗", withName("小明"), atUser("user_20002", "小红"))
	task := env.injectDone(t, env.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	block := memory.RenderPrivateBlock("私聊里聊过猫", memory.BlockEntry{Name: "小明", Tier: "熟悉", Note: "喜欢猫"})
	last := lastContent(env.short.Requests()[0])
	if !strings.HasPrefix(last, block+"\n\n") {
		t.Fatalf("last user message = %q, want it to start with the private block", last)
	}
	for _, banned := range []string{"群里最近", "别人的笔记", "52", "20001"} {
		if strings.Contains(last, banned) {
			t.Errorf("private request shows %q:\n%s", banned, last)
		}
	}
	if m := env.memberIn(t, key, "20001"); m.Affinity == nil || *m.Affinity != 53 {
		t.Errorf("private member after a reply = %+v, want affinity 53", m)
	}
}

func TestAffinity_PrivateChatCounts(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	task := env.injectDone(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "你好", withName("小明"))).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	if m := env.memberIn(t, privKeyOf("20001"), "20001"); m.Affinity == nil || *m.Affinity != 20 || m.DisplayName != "小明" {
		t.Fatalf("private member = %+v, want affinity 20 named 小明", m)
	}
	if rows := env.injectMembers(t); len(rows) != 0 {
		t.Fatalf("group rows = %+v, want none", rows)
	}
}

func TestMemoryCommand_PrivateChat(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	trig := &memCmdTrigger{}
	env.o.memTrigger = trig
	key := privKeyOf("20001")
	env.injectSeed(t, key, "", store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(52)})

	for _, c := range []struct{ text, want string }{
		{"/好感度", "织语对你的好感：熟悉（52/100）"},
		{"/记忆", "我记得关于你的：喜欢猫"},
		{"/记忆 摘要", CopyMemoryNoPrivateSummary},
		{"/memory summary", CopyMemoryNoPrivateSummary},
		{"/记忆 整理", CopyAdminOnly},
		{"/记忆 群", CopyMemoryUsagePrivate},
		{"/记忆 别的", CopyMemoryUsagePrivate},
	} {
		if got := env.privateCommand(t, "20001", c.text); got != c.want {
			t.Errorf("private %s = %q, want %q", c.text, got, c.want)
		}
	}
	if _, err := env.st.ApplyMemoryWrite(context.Background(), store.MemoryWrite{ConversationKey: key,
		Group: &store.GroupMemory{ConversationKey: key, Summary: "最近在聊钢琴。", CursorSeq: 7},
		Revisions: []store.MemoryRevision{{ConversationKey: key, Field: store.FieldSummary, NewValue: "最近在聊钢琴。",
			Source: store.RevisionConsolidate}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := env.privateCommand(t, "20001", "/记忆 摘要"), "我们的对话摘要：最近在聊钢琴。"; got != want {
		t.Errorf("private /记忆 摘要 = %q, want %q", got, want)
	}
	if calls := trig.calls(); len(calls) != 0 {
		t.Fatalf("non-admin private consolidation triggered %v", calls)
	}
	if got := env.privateCommand(t, "29999", "/记忆 整理"); got != CopyMemoryConsolidating {
		t.Errorf("admin private /记忆 整理 = %q", got)
	}
	if calls := trig.calls(); len(calls) != 1 || calls[0] != privKeyOf("29999") {
		t.Errorf("trigger calls = %v, want the admin's private chat", calls)
	}

	if got := env.privateCommand(t, "20001", "/忘记我"); got != CopyMemoryForgottenPrivate {
		t.Fatalf("private /忘记我 = %q, want %q", got, CopyMemoryForgottenPrivate)
	}
	if m := env.memberIn(t, key, "20001"); m.Note != "" || m.Affinity != nil || m.ForgottenAt.IsZero() {
		t.Errorf("forgotten private member = %+v", m)
	}
	g, err := env.st.GetGroupMemory(context.Background(), key)
	if err != nil || g.Summary != "" || g.CursorSeq != 7 {
		t.Errorf("private summary after /忘记我 = %+v, %v; want cleared with the cursor kept", g, err)
	}
	if revs, err := env.st.ListMemoryRevisions(context.Background(), key, ""); err != nil || len(revs) != 0 {
		t.Errorf("private summary revisions after /忘记我 = %+v, %v; want none", revs, err)
	}
}

func TestMemoryCommand_GroupForgetKeepsSummary(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	if _, err := env.st.ApplyMemoryWrite(context.Background(), store.MemoryWrite{ConversationKey: testConvKey,
		Group: &store.GroupMemory{ConversationKey: testConvKey, Summary: "大家在聊咖啡。"},
		Revisions: []store.MemoryRevision{{ConversationKey: testConvKey, Field: store.FieldSummary, NewValue: "大家在聊咖啡。",
			Source: store.RevisionConsolidate}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := env.command(t, "20001", "/忘记我"); got != CopyMemoryForgotten {
		t.Fatalf("/忘记我 = %q", got)
	}
	g, err := env.st.GetGroupMemory(context.Background(), testConvKey)
	revs, rerr := env.st.ListMemoryRevisions(context.Background(), testConvKey, "")
	if err != nil || rerr != nil || g.Summary != "大家在聊咖啡。" || len(revs) != 1 {
		t.Fatalf("group summary %+v and revisions %+v after /忘记我 (%v, %v); want both kept", g, revs, err, rerr)
	}
}

func TestMemoryCommand_PrivateChatDisabled(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(false)})
	key := privKeyOf("20001")
	env.injectSeed(t, key, "最近在聊钢琴。", store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(52)})
	for _, text := range []string{"/好感度", "/记忆", "/记忆 摘要", "/记住 我下周三考试"} {
		if got := env.privateCommand(t, "20001", text); got != CopyMemoryDisabledPrivate {
			t.Errorf("disabled private %s = %q, want %q", text, got, CopyMemoryDisabledPrivate)
		}
	}
	if got := env.privateCommand(t, "20001", "/忘记我"); got != CopyMemoryForgottenPrivate {
		t.Errorf("disabled private /忘记我 = %q", got)
	}
	if m := env.memberIn(t, key, "20001"); m.Note != "" || m.Affinity != nil {
		t.Errorf("forgotten private member = %+v", m)
	}
}

func TestMemoryCommand_GroupSummarySubcommand(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: memCmdConfig(true, true)})
	if err := env.st.PutGroupMemory(context.Background(), store.GroupMemory{ConversationKey: testConvKey, Summary: "大家在聊咖啡。"}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"/记忆 摘要", "/memory summary"} {
		if got := env.command(t, "20001", text); got != CopyAdminOnly {
			t.Errorf("non-admin group %s = %q, want %q", text, got, CopyAdminOnly)
		}
		if got, want := env.command(t, "29999", text), "群聊摘要：大家在聊咖啡。"; got != want {
			t.Errorf("admin group %s = %q, want %q", text, got, want)
		}
	}
}

func TestRemember_AppendsWithRevision(t *testing.T) {
	for _, c := range []struct {
		name    string
		key     string
		command func(env *testEnv, text string) string
	}{
		{"group", testConvKey, func(env *testEnv, text string) string { return env.command(t, "20001", text) }},
		{"private", privKeyOf("20001"), func(env *testEnv, text string) string { return env.privateCommand(t, "20001", text) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
			env.injectSeed(t, c.key, "", store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫。", Affinity: injectAffinity(40)})
			if got := c.command(env, "/记住  我下周三考试 "); got != CopyRemembered {
				t.Fatalf("/记住 = %q, want %q", got, CopyRemembered)
			}
			if got := c.command(env, "/remember 喜欢喝乌龙茶"); got != CopyRemembered {
				t.Fatalf("/remember = %q", got)
			}
			m := env.memberIn(t, c.key, "20001")
			if want := "喜欢猫。（他让我记住）我下周三考试（他让我记住）喜欢喝乌龙茶"; m.Note != want || m.Affinity == nil || *m.Affinity != 40 {
				t.Fatalf("member = %+v, want note %q and affinity 40", m, want)
			}
			revs, err := env.st.ListMemoryRevisions(context.Background(), c.key, "20001")
			if err != nil {
				t.Fatal(err)
			}
			if len(revs) != 2 || revs[0].Source != store.RevisionCommand || revs[0].Reason != memory.ReasonRemember ||
				revs[0].Field != store.FieldNote || revs[0].OldValue != "喜欢猫。" || revs[1].NewValue != m.Note {
				t.Fatalf("revisions = %+v, want two command/remember note revisions", revs)
			}
		})
	}
}

func TestRemember_TrimsOldestToTheLimit(t *testing.T) {
	sentence := strings.Repeat("甲", 30) + "。"
	for _, c := range []struct {
		name  string
		key   string
		limit int
		group bool
	}{
		{"group", testConvKey, memory.NoteMaxRunes, true},
		{"private", privKeyOf("20001"), memory.PrivateNoteMaxRunes, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
			old := strings.Repeat(sentence, c.limit/31)
			env.injectSeed(t, c.key, "", store.MemberMemory{PlatformUserID: "20001", Note: old})
			content := strings.Repeat("乙", memory.RememberMaxRunes)
			var got string
			if c.group {
				got = env.command(t, "20001", "/记住 "+content)
			} else {
				got = env.privateCommand(t, "20001", "/记住 "+content)
			}
			if got != CopyRemembered {
				t.Fatalf("/记住 = %q", got)
			}
			m := env.memberIn(t, c.key, "20001")
			if want := memory.AppendRemembered(old, content, c.limit); m.Note != want {
				t.Fatalf("note = %q, want %q", m.Note, want)
			}
			if n := utf8.RuneCountInString(m.Note); n > c.limit || !strings.HasSuffix(m.Note, memory.RememberMarker+content) ||
				!strings.HasPrefix(m.Note, sentence) {
				t.Fatalf("note has %d runes (limit %d) and must keep whole sentences and end with the new item: %q", n, c.limit, m.Note)
			}
		})
	}
}

func TestRemember_UsageTooLongAndDisabled(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	for _, c := range []struct{ text, want string }{
		{"/记住", CopyRememberUsage},
		{"/记住    ", CopyRememberUsage},
		{"/remember", CopyRememberUsage},
		{"/记住 " + strings.Repeat("长", memory.RememberMaxRunes+1), CopyRememberTooLong},
		{"/记住 " + strings.Repeat("长", memory.RememberMaxRunes), CopyRemembered},
	} {
		if got := env.command(t, "20001", c.text); got != c.want {
			t.Errorf("%.20s… = %q, want %q", c.text, got, c.want)
		}
	}
	if m := env.memberIn(t, testConvKey, "20001"); m.Note != memory.RememberMarker+strings.Repeat("长", memory.RememberMaxRunes) {
		t.Errorf("note = %q, want only the accepted item", m.Note)
	}

	off := newTestOrchestrator(t, testOpts{configure: privMemConfig(false)})
	if got := off.command(t, "20001", "/记住 我下周三考试"); got != CopyMemoryDisabled {
		t.Errorf("disabled group /记住 = %q, want %q", got, CopyMemoryDisabled)
	}
	if rows := off.injectMembers(t); len(rows) != 0 {
		t.Errorf("disabled /记住 wrote %+v", rows)
	}
}

func TestRemember_KeepsForgottenAt(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	if got := env.command(t, "20001", "/忘记我"); got != CopyMemoryForgotten {
		t.Fatalf("/忘记我 = %q", got)
	}
	forgotten := env.memberIn(t, testConvKey, "20001").ForgottenAt
	if got := env.command(t, "20001", "/记住 我下周三考试"); got != CopyRemembered {
		t.Fatalf("/记住 = %q", got)
	}
	if m := env.memberIn(t, testConvKey, "20001"); !m.ForgottenAt.Equal(forgotten) || m.Note != memory.RememberMarker+"我下周三考试" {
		t.Fatalf("member = %+v, want forgotten_at %v kept", m, forgotten)
	}
}

func TestDecide_RememberCommandKnown(t *testing.T) {
	for _, word := range []string{"记住", "remember"} {
		in := DecideInput{
			BotInstanceID: testBot, SelfPlatformID: "30001", ConversationType: conloomapi.ConvGroup,
			PlatformUserID: "20001", Text: "/" + word + " 我下周三考试", CommandPrefix: "/", Enabled: true,
		}
		want := Decision{Disposition: conloomapi.DispositionReply, Trigger: conloomapi.TriggerCommand, Command: word, Args: "我下周三考试"}
		if got := Decide(in); got != want {
			t.Errorf("Decide(/%s) = %+v, want %+v", word, got, want)
		}
	}
}
