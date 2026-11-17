package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// The /help text is built from the registry and reads as it always did.
func TestCommandRegistry_HelpText(t *testing.T) {
	want := "我是织语。在群里 @我 或回复我的消息就能和我聊天。\n命令：/help 帮助，/stop 停止当前回复，/persona 查看人格，/主动 查看或设置主动搭话，/好感度 查看好感度，/记忆 查看我记得的你，/记住 <内容> 让我记住一件事，/忘记我 清空你的记忆；管理员：/pause 暂停，/resume 恢复，/persona list 列出人格模板，/persona use <模板名> 切换人格模板，/记忆 整理 立即整理记忆，/记忆 群 查看群聊摘要。"
	if got := CopyHelp("织语"); got != want {
		t.Errorf("CopyHelp = %q\nwant %q", got, want)
	}
}

// Every word execCommand runs is recognised, from the registry, and nothing
// else is; every entry is well formed.
func TestCommandRegistry_Words(t *testing.T) {
	dispatched := []string{"help", "persona", "stop", "pause", "resume", "主动", "proactive",
		"好感度", "affinity", "记忆", "memory", "记住", "remember", "忘记我", "forgetme"}
	got := slices.Clone(KnownCommands)
	slices.Sort(got)
	slices.Sort(dispatched)
	if !slices.Equal(got, dispatched) {
		t.Errorf("KnownCommands = %q, want the words execCommand runs %q", got, dispatched)
	}
	for _, w := range KnownCommands {
		if cmd, _, ok := ParseCommand("/"+w, "/"); !ok || cmd != w {
			t.Errorf("ParseCommand(/%s) = %q, %v", w, cmd, ok)
		}
	}
	aliases := map[string][]string{}
	seen := map[string]bool{}
	for _, c := range commandRegistry {
		text := c.name
		if c.form != "" {
			text += " " + c.form
		}
		if seen[text] {
			t.Errorf("%q is listed twice", text)
		}
		seen[text] = true
		if !c.group && !c.private {
			t.Errorf("%q applies to no conversation", text)
		}
		if c.desc == "" || strings.ContainsAny(c.desc, "/\n") {
			t.Errorf("%q: description %q", text, c.desc)
		}
		if c.usage != "/"+text && !(c.args && strings.HasPrefix(c.usage, "/"+text+" ")) {
			t.Errorf("%q: usage %q does not start with /%s (and is followed by arguments only when it takes them)", text, c.usage, text)
		}
		if a, ok := aliases[c.name]; ok && !slices.Equal(a, c.aliases) {
			t.Errorf("%q: aliases %q, other entries of %s have %q", text, c.aliases, c.name, a)
		}
		aliases[c.name] = c.aliases
	}
	for name := range aliases {
		if !slices.ContainsFunc(commandRegistry, func(c command) bool { return c.name == name && c.form == "" }) {
			t.Errorf("%q has sub-forms but no entry of its own", name)
		}
	}
}

// Each recognised word and each menu entry runs in the conversations it
// applies to: an admin sending it gets neither a failure, a usage nor a
// refusal.
func TestCommandRegistry_EveryEntryRuns(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	sample := map[string]string{
		"记住": "我下周三考试", "persona use": env.o.personas.TemplateNames()[0], "主动 全群": "开", "好感度 设": "50",
	}
	bad := []string{CopyUnavailable, CopyAdminOnly, CopyPersonaUsage, CopyProactiveUsage, CopyPrivateProactiveUsage,
		CopyAffinityUsage, CopyMemoryUsage, CopyMemoryUsagePrivate, CopyRememberUsage,
		CopyMemoryDisabled, CopyMemoryDisabledPrivate, CopyAffinityOff, CopyAffinityOffPrivate, CopyMemoryPaused, CopyMemoryPausedPrivate}
	run := map[string]func(t *testing.T, user, text string) string{
		conloomapi.ConvGroup: env.command, conloomapi.ConvPrivate: env.privateCommand,
	}
	for _, convType := range []string{conloomapi.ConvGroup, conloomapi.ConvPrivate} {
		for _, w := range KnownCommands {
			text := "/" + w
			if w == "记住" || w == "remember" {
				text += " " + sample["记住"]
			}
			if got := run[convType](t, "29999", text); slices.Contains(bad, got) {
				t.Errorf("%s %s = %q", convType, text, got)
			}
		}
		for _, m := range MenuCommands("/", convType, true) {
			text := "/" + m.Name
			if m.Args {
				text += " " + sample[m.Name]
			}
			if got := run[convType](t, "29999", text); slices.Contains(bad, got) {
				t.Errorf("%s %s = %q", convType, text, got)
			}
		}
	}
}

func menuNames(cmds []MenuCommand) []string {
	out := []string{}
	for _, c := range cmds {
		out = append(out, c.Name)
	}
	return out
}

func TestMenuCommands_Filter(t *testing.T) {
	for _, c := range []struct {
		convType string
		admin    bool
		want     []string
	}{
		{conloomapi.ConvGroup, false, []string{"help", "stop", "persona", "主动", "好感度", "记忆", "记住", "忘记我"}},
		{conloomapi.ConvGroup, true, []string{"help", "stop", "persona", "主动", "好感度", "记忆", "记住", "忘记我",
			"pause", "resume", "persona list", "persona use", "主动 全群", "好感度 设", "记忆 整理", "记忆 群"}},
		{conloomapi.ConvPrivate, false, []string{"help", "stop", "persona", "主动", "好感度", "记忆", "记忆 摘要", "记住", "忘记我"}},
		{conloomapi.ConvPrivate, true, []string{"help", "stop", "persona", "主动", "好感度", "记忆", "记忆 摘要", "记住", "忘记我",
			"pause", "resume", "persona list", "persona use", "好感度 设", "记忆 整理"}},
		{"channel", true, []string{}},
	} {
		if got := menuNames(MenuCommands("/", c.convType, c.admin)); !slices.Equal(got, c.want) {
			t.Errorf("MenuCommands(%s, admin %v) = %q, want %q", c.convType, c.admin, got, c.want)
		}
	}

	cmds := MenuCommands("!", conloomapi.ConvPrivate, true)
	i := slices.IndexFunc(cmds, func(m MenuCommand) bool { return m.Name == "记住" })
	if i < 0 || cmds[i].Usage != "!记住 <内容>" || !cmds[i].Args || cmds[i].Description != "让我记住一件事" ||
		!slices.Equal(cmds[i].Aliases, []string{"remember"}) {
		t.Errorf("记住 with prefix ! = %+v", cmds)
	}
	if i := slices.IndexFunc(cmds, func(m MenuCommand) bool { return m.Name == "help" }); i < 0 || cmds[i].Args || cmds[i].Aliases == nil {
		t.Errorf("help = %+v, want no arguments and an empty alias list", cmds)
	}
	if got := MenuCommands("", conloomapi.ConvGroup, true); len(got) != 0 {
		t.Errorf("MenuCommands without a prefix = %+v, want none", got)
	}
}
