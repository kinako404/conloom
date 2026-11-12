package core

import "strconv"

func CopyHelp(botName string) string {
	return "我是" + botName + "。在群里 @我 或回复我的消息就能和我聊天。\n命令：/help 帮助，/stop 停止当前回复，/persona 查看人格；管理员：/pause 暂停，/resume 恢复。"
}

func CopyPersonaInfo(name string, version int) string {
	return "当前人格：" + name + "（第 " + strconv.Itoa(version) + " 版）"
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

	// CopyDirectiveContinuation is appended to the current message of a
	// continuation task.
	CopyDirectiveContinuation = "【提示】这条消息没有 @ 你，只是对方在你回复后接着说的。如果它不是在和你说话，或者话题已经转向别人，只输出 [[SKIP]]，不要输出其他内容。"
)
