package core

import (
	"strconv"
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func CopyHelp(botName string) string {
	return "我是" + botName + "。在群里 @我 或回复我的消息就能和我聊天。\n命令：/help 帮助，/stop 停止当前回复，/persona 查看人格，/主动 查看或设置主动搭话；管理员：/pause 暂停，/resume 恢复，/persona list 列出人格模板，/persona use <模板名> 切换人格模板。"
}

// CopyPersonaInfo describes the current persona: its name and version, and
// its character name when it has one.
func CopyPersonaInfo(name string, version int, character string) string {
	s := "当前人格：" + name + "（第 " + strconv.Itoa(version) + " 版）"
	if character != "" {
		s += "，角色名：" + character
	}
	return s
}

const CopyPersonaUsage = "用法：/persona 查看当前人格；管理员：/persona list 列出人格模板，/persona use <模板名> 切换人格模板。"

// CopyPersonaTemplates lists the persona templates an admin can apply.
func CopyPersonaTemplates(names []string) string {
	return "可用的人格模板：" + strings.Join(names, "、") + "。发送 /persona use <模板名> 切换。"
}

// CopyPersonaUnknownTemplate answers /persona use with a template that does
// not exist.
func CopyPersonaUnknownTemplate(name string, names []string) string {
	return "没有名为 " + name + " 的人格模板。可用的人格模板：" + strings.Join(names, "、") + "。"
}

// CopyPersonaApplied confirms that template is now the persona, as version.
func CopyPersonaApplied(template string, version int) string {
	return "已切换到人格模板 " + template + "（第 " + strconv.Itoa(version) + " 版）。"
}

const (
	CopyStopDone      = "好的，已停止。"
	CopyStopNothing   = "现在没有进行中的回复。"
	CopyStopForbidden = "只能停止你自己发起的回复。"
	CopyAdminOnly     = "只有管理员可以这样做。"
	CopyPaused        = "已暂停回复。管理员发送 /resume 可以恢复。"
	CopyResumed       = "已恢复回复。"
	CopyTooLong       = "这段内容太长了，我一次处理不了，可以分几段发给我吗？"
	CopyUnavailable   = "我这边暂时有点卡住了，稍后再试试吧。"

	CopyProactiveNotEligible = "先 @ 我聊几句，才能开启主动搭话。"
	CopyProactivePrivateOnly = "主动搭话只在群聊里使用。"
	CopyProactiveGroupOn     = "已开启本群的主动搭话。"
	CopyProactiveGroupOff    = "已关闭本群的主动搭话。"
	CopyProactiveUsage       = "用法：/主动 查看档位；/主动 关|低|中|高 设置档位；管理员：/主动 全群 开|关。"
)

// CopyDirectiveContinuation is appended to the current message of a turn in
// the bot's conversation with speaker.
func CopyDirectiveContinuation(speaker string) string {
	return "【提示】你正在和 " + speaker + " 聊天，下面是他在你上次回复后说的话（可能有好几句）。你不需要每句都回应：只有当他在问你、在等你回应，或者你有明显值得说的话时，才回复一两句（单纯附和，比如“确实”“是啊”“哈哈”，不算值得说的话）；如果他是在和别人说话、话题已经转开，或者没必要回应，只输出 [[SKIP]]，不要输出其他内容。"
}

// CopyDirectiveJoin is appended to the current message of a task joining the
// group's conversation: level is the proactive level of target, the sender of
// that message (a higher level makes the bot more willing to join; off and
// unknown levels get the low wording), names the eligible members in the
// conversation and terms their top interest terms. Without terms the sentence
// naming them is left out.
func CopyDirectiveJoin(level, target string, names, terms []string) string {
	var s string
	switch level {
	case conloomapi.ProactiveHigh:
		s = "【提示】群里正在闲聊（见上面的近期消息），没有人 @ 你。你是群里的一员，可以像群友一样自然地插一句：接梗、回应、调侃或提个问题都行，一两句即可（必要时会引用 " +
			target + " 的这条消息）。" + target + " 把主动搭话设成了“高”，表示很欢迎你多参与。只有当大家明显在谈正事、或你实在没什么可说时，才只输出 [[SKIP]]，不要输出其他内容。"
	case conloomapi.ProactiveMid:
		s = "【提示】群里正在闲聊（见上面的近期消息），没有人 @ 你。你是群里的一员，如果能自然地接上话题、说点有意思或有帮助的话，就插一句，一两句即可（必要时会引用 " +
			target + " 的这条消息）；如果不太合适，或者大家明显在和别人谈正事，就只输出 [[SKIP]]，不要输出其他内容。"
	default:
		s = "【提示】群里大家正在聊天（见上面的近期消息），没有人 @ 你。只有当你能自然地接上正在聊的话题、对大家有帮助或有趣，而且不会打断别人时，才说一两句（必要时会引用 " +
			target + " 的这条消息）；不合适就只输出 [[SKIP]]，不要输出其他内容。"
	}
	if len(terms) > 0 {
		s += strings.Join(names, "、") + " 以前常和你聊：" + strings.Join(terms, "、") + "。"
	}
	return s
}

// CopyProactiveStatus describes a member's proactive setting: level and
// eligibility, how often the bot spoke up to them today out of their daily
// limit, and whether the group switch is on.
func CopyProactiveStatus(level string, eligible bool, sentToday, dailyLimit int, groupEnabled bool) string {
	var b strings.Builder
	if eligible {
		b.WriteString("你的主动搭话档位：" + levelName(level) + "，今天已主动找你聊 " +
			strconv.Itoa(sentToday) + "/" + strconv.Itoa(dailyLimit) + " 次。可用 /主动 关|低|中|高 调整。")
	} else {
		b.WriteString("你还没有主动搭话资格，" + CopyProactiveNotEligible)
	}
	if !groupEnabled {
		b.WriteString("本群的主动搭话目前已关闭。")
	}
	return b.String()
}

// CopyProactiveSet confirms a member's new proactive level.
func CopyProactiveSet(level string) string {
	return "好的，你的主动搭话档位已设为：" + levelName(level) + "。"
}

func levelName(level string) string {
	switch level {
	case conloomapi.ProactiveOff:
		return "关"
	case conloomapi.ProactiveLow:
		return "低"
	case conloomapi.ProactiveMid:
		return "中"
	case conloomapi.ProactiveHigh:
		return "高"
	}
	return level
}
