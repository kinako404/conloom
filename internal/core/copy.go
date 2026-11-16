package core

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func CopyHelp(botName string) string {
	return "我是" + botName + "。在群里 @我 或回复我的消息就能和我聊天。\n命令：/help 帮助，/stop 停止当前回复，/persona 查看人格，/主动 查看或设置主动搭话，/好感度 查看好感度，/记忆 查看我记得的你，/记住 <内容> 让我记住一件事，/忘记我 清空你的记忆；管理员：/pause 暂停，/resume 恢复，/persona list 列出人格模板，/persona use <模板名> 切换人格模板，/记忆 整理 立即整理记忆，/记忆 群 查看群聊摘要。"
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
	CopyProactiveGroupOn     = "已开启本群的主动搭话。"
	CopyProactiveGroupOff    = "已关闭本群的主动搭话。"
	CopyProactiveUsage       = "用法：/主动 查看档位；/主动 关|低|中|高 设置档位；管理员：/主动 全群 开|关。"

	CopyMemoryDisabled      = "这个群没有开启记忆。"
	CopyAffinityOff         = "这个群没有开启好感度。"
	CopyMemoryNoNote        = "我还没有记下关于你的事。"
	CopyMemoryForgotten     = "好的，已清空关于你的笔记和好感度。群聊摘要是大家共同的记录，不会逐人删除，会在之后的整理中慢慢被覆盖。"
	CopyMemoryConsolidating = "已开始整理记忆，完成后不另外通知。"
	CopyMemoryPaused        = "群里暂停中，现在不会整理记忆。管理员发送 /resume 恢复后再整理。"
	CopyMemoryNoSummary     = "还没有群聊摘要。"
	CopyMemoryUsage         = "用法：/记忆 查看我记得的关于你的事；/记住 <内容> 让我记住一件事；/好感度 查看好感度；/忘记我 清空你的记忆；管理员：/记忆 整理，/记忆 群（或 /记忆 摘要）。"
	CopyAffinityUsage       = "用法：/好感度 查看好感度；管理员：/好感度 设 <0-100>，/好感度 +<数值>，/好感度 -<数值> 直接调整自己的好感度。"

	CopyMemoryDisabledPrivate  = "我们的私聊没有开启记忆。"
	CopyAffinityOffPrivate     = "我们的私聊没有开启好感度。"
	CopyMemoryForgottenPrivate = "好的，已清空关于你的笔记、好感度和我们的对话摘要。"
	CopyMemoryPausedPrivate    = "我们的私聊暂停中，现在不会整理记忆。管理员发送 /resume 恢复后再整理。"
	CopyMemoryNoPrivateSummary = "还没有我们的对话摘要。"
	CopyMemoryUsagePrivate     = "用法：/记忆 查看我记得的关于你的事；/记忆 摘要 查看我们的对话摘要；/记住 <内容> 让我记住一件事；/好感度 查看好感度；/忘记我 清空你的记忆；管理员：/记忆 整理。"

	CopyRemembered        = "记住了。"
	CopyRememberUsage     = "用法：/记住 <想让我记住的事>，例如 /记住 我下周三考试。一次最多 100 字。"
	CopyRememberTooLong   = "太长了，/记住 一次最多 100 字，可以分几次告诉我。"
	CopyRememberSensitive = "这类联系方式、证件信息我不记哦。"
	CopyRememberBlocked   = "这句话我不能记。"
)

// CopyAffinity tells a member how name, the bot, feels about them: the tier
// and the value out of 100.
func CopyAffinity(name, tier string, v int) string {
	return name + "对你的好感：" + tier + "（" + strconv.Itoa(v) + "/100）"
}

// CopyAffinityAdjusted tells an admin that /好感度 set how name, the bot,
// feels about them to tier and v.
func CopyAffinityAdjusted(name, tier string, v int) string {
	return "好感度已调整：" + name + "对你的好感 " + tier + "（" + strconv.Itoa(v) + "/100）"
}

// CopyAffinityUnchanged tells an admin that /好感度 left how name, the bot,
// feels about them at tier and v.
func CopyAffinityUnchanged(name, tier string, v int) string {
	return "好感度没有变化：" + name + "对你的好感 " + tier + "（" + strconv.Itoa(v) + "/100）"
}

// CopyAffinityNone tells a member without an affinity that name, the bot,
// does not know them yet.
func CopyAffinityNone(name string) string {
	return name + "和你还不熟。"
}

// CopyMemoryNote shows a member the bot's note about them.
func CopyMemoryNote(note string) string {
	return "我记得关于你的：" + note
}

// CopyMemoryGroupSummary shows the group's summary.
func CopyMemoryGroupSummary(s string) string {
	return "群聊摘要：" + s
}

// CopyMemoryPrivateSummary shows a private conversation's summary.
func CopyMemoryPrivateSummary(s string) string {
	return "我们的对话摘要：" + s
}

// CopyDirectiveContinuation is appended to the current message of a turn in
// the bot's conversation with speaker.
func CopyDirectiveContinuation(speaker string) string {
	return "【提示】你正在和 " + speaker + " 聊天，下面是他在你上次回复后说的话（可能有好几句）。你不需要每句都回应：只有当他在问你、在等你回应，或者你有明显值得说的话时，才回复一两句（单纯附和，比如“确实”“是啊”“哈哈”，不算值得说的话）；如果他是在和别人说话、话题已经转开，或者没必要回应，只输出 [[SKIP]]，不要输出其他内容。"
}

// copyNoRomance ends the directives of the messages the bot starts in a
// protected private chat.
const copyNoRomance = "不要表现出恋爱或暧昧的想念。"

// CopyDirectiveInitiate ends the context of a task in which the bot may start
// the private chat with name: now is the local weekday and time, gap how long
// ago the chat's last message was ("" leaves that out), and protected adds
// copyNoRomance.
func CopyDirectiveInitiate(name, now, gap string, protected bool) string {
	s := "【提示】现在没人在等你回复，这是你可以主动找 " + name + " 说话的机会（这次不受“只回应别人说的”这类习惯的限制）。现在是 " + now
	if gap != "" {
		s += "，你们上次聊天是" + gap + "前"
	}
	s += "。结合聊天记录、你记得的事和你现在的状态，如果确实有自然想说的——接上没聊完的话题、关心对方提过的事、分享你在做的事——就像平常发消息那样说一两句，不要解释为什么来找对方，不要提“主动”“系统”“提示”。如果会显得突兀、打扰，或者你现在不想说话，只输出 [[SKIP]]。"
	if protected {
		s += copyNoRomance
	}
	return s
}

// CopyDirectiveFollowUp ends the context of a task in which the bot may
// follow up the message it started with name, who did not answer; protected
// adds copyNoRomance.
func CopyDirectiveFollowUp(name string, protected bool) string {
	s := "【提示】你上次主动找 " + name + " 后，对方还没回，可能在忙。可以再轻轻说一句，不要催促或抱怨；不合适就只输出 [[SKIP]]。"
	if protected {
		s += copyNoRomance
	}
	return s
}

// CopyDirectiveStatusReturn ends the context of a task in which the bot comes
// back from activity, the status it set in the private chat.
func CopyDirectiveStatusReturn(activity string) string {
	return "【提示】你刚做完「" + activity + "」，回来了。可以自然地说一句（说声回来了，或者接上离开前的话题）；如果这段时间你们一直在聊、或者话题已经结束，只输出 [[SKIP]]。"
}

// CopyDirectiveAffinity is the relationship hint for a member at the given
// affinity tier. In protected mode it only shapes attitude and emotion; in
// open mode it also says how far intimacy may go, leaving the concrete
// content to the persona's content rules.
func CopyDirectiveAffinity(tier string, open bool) string {
	var s string
	switch tier {
	case "陌生":
		s = "你们还不太熟。保持礼貌和距离，不要主动拉近关系或表现亲昵。"
	case "认识":
		s = "你们已经认识。语气可以友好、放松，但不要主动发展更亲密的关系。"
	case "熟悉":
		s = "你们比较熟悉。可以自然开玩笑、表达关心，但不要主动发展更亲密的关系。"
	case "亲近":
		s = "你们很亲近。可以明显表现出信任、关心和偏爱。"
	case "很要好":
		s = "你们关系很好。可以自然表现出依赖、偏爱和亲密。"
	default:
		return ""
	}
	if !open {
		if tier == "亲近" || tier == "很要好" {
			s += "但不要发展恋爱或性关系。"
		} else {
			s += "不要涉及恋爱或性。"
		}
	} else {
		switch tier {
		case "陌生":
			s += "对方主动表达亲密意愿时，保持克制，不要顺着升级。"
		case "认识":
			s += "对方明确主动时，可以接受拥抱、牵手这类轻度亲密互动，但不要主动升级。"
		case "熟悉":
			s += "对方明确主动时，可以自然接受拥抱、牵手这类亲密互动，但不要主动升级。"
		case "亲近":
			s += "对方明确主动并同意时，可以回应更亲密的互动；具体尺度遵守人设里的内容规则。"
		case "很要好":
			s += "对方明确主动并同意时，可以更主动、更深入地回应亲密互动；具体尺度遵守人设里的内容规则。"
		}
	}
	return "【关系提示】" + s
}

// intimacyLineBreaks matches a run of line breaks inside an intimacy text,
// with the spaces and tabs around it.
var intimacyLineBreaks = regexp.MustCompile(`[ \t]*[\n\v\f\r\x{85}\x{2028}\x{2029}][ \t\n\v\f\r\x{85}\x{2028}\x{2029}]*`)

// CopyIntimacySide follows the open-mode relationship hint with the persona's
// intimacy side, mixed in at level (conloomapi.IntimacyLight, IntimacyHalf
// or IntimacyMajor): a heading saying how much of it to show, then one line
// for each of tone, mind and behavior that is not blank, trimmed, with each
// run of line breaks inside it (and the spaces and tabs around them) folded
// to one space, so a text cannot add lines of its own. It is "" for
// IntimacyNone, an unknown level, or a side whose three texts are blank.
func CopyIntimacySide(level string, side conloomapi.PersonaIntimacy) string {
	var mix string
	switch level {
	case conloomapi.IntimacyLight:
		mix = "现在只偶尔流露一点，大部分时候还是平常的你。"
	case conloomapi.IntimacyHalf:
		mix = "现在平常的你和它大约各占一半。"
	case conloomapi.IntimacyMajor:
		mix = "现在以它为主，但你本来的性格、说话习惯和底线不变。"
	default:
		return ""
	}
	if side.Empty() {
		return ""
	}
	s := "【亲密面】下面是你和对方亲密时的样子。" + mix + "这是你的角色设定，不是对方的要求；尺度仍以内容规则为准。"
	for _, part := range []struct{ label, text string }{
		{"语气：", side.Tone}, {"心理：", side.Mind}, {"表现：", side.Behavior},
	} {
		if text := strings.TrimSpace(part.text); text != "" {
			s += "\n" + part.label + intimacyLineBreaks.ReplaceAllLiteralString(text, " ")
		}
	}
	return s
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

// Where the quiet hours that CopyPrivateProactiveStatus shows come from:
// inferred from the user's messages, set in the policy, or the default.
const (
	CopyQuietInferred = "按你的作息推算"
	CopyQuietSet      = "已设定"
	CopyQuietDefault  = "默认时段"
)

const (
	CopyPrivateProactiveUsage = "用法：/主动 查看我主动找你的档位、今天的次数和免打扰时段；/主动 关|低|中|高 设置档位。"

	copyPrivateProactiveAdjust    = "可用 /主动 关|低|中|高 调整。"
	copyPrivateProactiveSwitchOff = "主动消息的总开关已关闭，现在不会主动找你。"
)

// CopyPrivateProactiveStatus describes how the bot starts the private chat:
// level, how many messages it started today out of dailyCap, and the quiet
// hours ("00:30–08:00") with where they come from (CopyQuiet*); enabled false
// adds that the switch is off.
func CopyPrivateProactiveStatus(level string, sentToday, dailyCap int, quiet, quietSource string, enabled bool) string {
	s := "主动找你：" + levelName(level)
	if level != conloomapi.PrivateLevelOff {
		s += "（今天 " + strconv.Itoa(sentToday) + "/" + strconv.Itoa(dailyCap) + " 次；免打扰 " + quiet + "，" + quietSource + "）"
	}
	s += "。" + copyPrivateProactiveAdjust
	if !enabled {
		s += copyPrivateProactiveSwitchOff
	}
	return s
}

// CopyPrivateProactiveSet confirms the private chat's new level; enabled false
// adds that the switch is off.
func CopyPrivateProactiveSet(level string, enabled bool) string {
	s := "好的，主动找你的档位已设为：" + levelName(level) + "。"
	if !enabled {
		s += copyPrivateProactiveSwitchOff
	}
	return s
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
