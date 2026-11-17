package core

import (
	"slices"
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// command is an entry of the command registry: a command execCommand runs or
// one of its sub-forms worth picking directly from a command menu.
type command struct {
	// name is the command word, the Chinese one when there is one, and
	// aliases the other words for the same command; ParseCommand recognises
	// both (see KnownCommands).
	name    string
	aliases []string
	// form is the fixed argument of a sub-form, such as 整理 of /记忆 整理;
	// "" for the command itself.
	form string
	// usage is how the command is sent, with "/" for the prefix, and desc
	// what it does, worded as in /help.
	usage, desc string
	// args is set when arguments are typed after the name and form.
	args bool
	// admin is set when only admins may run it.
	admin bool
	// group and private are the conversation types it applies to.
	group, private bool
	// help lists it in /help (see CopyHelp).
	help bool
}

// commandRegistry lists the commands and their useful sub-forms: the ones
// everyone may run first, then the admins'. It is the single list behind
// command recognition (KnownCommands), /help (CopyHelp) and the panel's
// command menu (MenuCommands).
var commandRegistry = []command{
	{name: "help", usage: "/help", desc: "帮助", group: true, private: true, help: true},
	{name: "stop", usage: "/stop", desc: "停止当前回复", group: true, private: true, help: true},
	{name: "persona", usage: "/persona", desc: "查看人格", group: true, private: true, help: true},
	{name: "主动", aliases: []string{"proactive"}, usage: "/主动", desc: "查看或设置主动搭话", group: true, private: true, help: true},
	{name: "好感度", aliases: []string{"affinity"}, usage: "/好感度", desc: "查看好感度", group: true, private: true, help: true},
	{name: "记忆", aliases: []string{"memory"}, usage: "/记忆", desc: "查看我记得的你", group: true, private: true, help: true},
	{name: "记忆", aliases: []string{"memory"}, form: "摘要", usage: "/记忆 摘要", desc: "查看我们的对话摘要", private: true},
	{name: "记住", aliases: []string{"remember"}, usage: "/记住 <内容>", desc: "让我记住一件事", args: true, group: true, private: true, help: true},
	{name: "忘记我", aliases: []string{"forgetme"}, usage: "/忘记我", desc: "清空你的记忆", group: true, private: true, help: true},

	{name: "pause", usage: "/pause", desc: "暂停", admin: true, group: true, private: true, help: true},
	{name: "resume", usage: "/resume", desc: "恢复", admin: true, group: true, private: true, help: true},
	{name: "persona", form: "list", usage: "/persona list", desc: "列出人格模板", admin: true, group: true, private: true, help: true},
	{name: "persona", form: "use", usage: "/persona use <模板名>", desc: "切换人格模板", args: true, admin: true, group: true, private: true, help: true},
	{name: "主动", aliases: []string{"proactive"}, form: "全群", usage: "/主动 全群 开|关", desc: "开关本群的主动搭话", args: true, admin: true, group: true},
	{name: "好感度", aliases: []string{"affinity"}, form: "设", usage: "/好感度 设 <0-100>", desc: "直接调整自己的好感度", args: true, admin: true, group: true, private: true},
	{name: "记忆", aliases: []string{"memory"}, form: "整理", usage: "/记忆 整理", desc: "立即整理记忆", admin: true, group: true, private: true, help: true},
	{name: "记忆", aliases: []string{"memory"}, form: "群", usage: "/记忆 群", desc: "查看群聊摘要", admin: true, group: true, help: true},
}

// KnownCommands are the command words ParseCommand recognises: the names and
// aliases of the command registry.
var KnownCommands = knownCommands()

func knownCommands() []string {
	var out []string
	for _, c := range commandRegistry {
		for _, w := range append([]string{c.name}, c.aliases...) {
			if !slices.Contains(out, w) {
				out = append(out, w)
			}
		}
	}
	return out
}

// MenuCommand is a command as a command menu offers it.
type MenuCommand struct {
	// Name is the command word followed by the fixed argument of a sub-form,
	// without the prefix ("记忆 整理"), and Aliases the other words for the
	// command.
	Name    string
	Aliases []string
	// Usage is how it is sent, with the conversation's prefix.
	Usage       string
	Description string
	// Args is set when arguments are typed after Name.
	Args bool
}

// MenuCommands lists the commands a member of a conversation of convType
// (conloomapi.ConvGroup or ConvPrivate) may run, with the conversation's
// command prefix, in the registry's order: admin-only ones only when isAdmin.
// An empty prefix turns commands off, so there are none.
func MenuCommands(prefix, convType string, isAdmin bool) []MenuCommand {
	if prefix == "" {
		return nil
	}
	var out []MenuCommand
	for _, c := range commandRegistry {
		if c.admin && !isAdmin || !c.appliesTo(convType) {
			continue
		}
		name := c.name
		if c.form != "" {
			name += " " + c.form
		}
		out = append(out, MenuCommand{
			Name: name, Aliases: append([]string{}, c.aliases...), Usage: prefix + strings.TrimPrefix(c.usage, "/"),
			Description: c.desc, Args: c.args,
		})
	}
	return out
}

func (c command) appliesTo(convType string) bool {
	switch convType {
	case conloomapi.ConvGroup:
		return c.group
	case conloomapi.ConvPrivate:
		return c.private
	}
	return false
}

// helpCommands is the command list of CopyHelp: each command the registry
// lists in /help as its usage and description, the ones everyone may run
// first, then, after 管理员, the admins'.
func helpCommands() string {
	var everyone, admins []string
	for _, c := range commandRegistry {
		if !c.help {
			continue
		}
		if c.admin {
			admins = append(admins, c.usage+" "+c.desc)
		} else {
			everyone = append(everyone, c.usage+" "+c.desc)
		}
	}
	return "命令：" + strings.Join(everyone, "，") + "；管理员：" + strings.Join(admins, "，") + "。"
}
