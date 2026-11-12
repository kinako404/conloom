package persona

import (
	_ "embed"
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

//go:embed core.md
var coreTemplate string

// CorePrompt returns the immutable core persona with {bot_name} replaced by botName.
func CorePrompt(botName string) string {
	return strings.ReplaceAll(strings.TrimRight(coreTemplate, "\n"), "{bot_name}", botName)
}

// level maps a style value to one of three phrasings: below 0.34, below 0.67, otherwise.
func level(v float64, low, mid, high string) string {
	switch {
	case v < 0.34:
		return low
	case v < 0.67:
		return mid
	default:
		return high
	}
}

var (
	replyLengthText = map[string]string{
		"short":  "默认回复简短，一两句话就好。",
		"medium": "默认回复长度适中，几句话说清楚。",
		"long":   "默认回复可以长一些，把事情讲完整。",
	}
	followupText = map[string]string{
		"never":        "不要反问对方。",
		"occasionally": "只在确实需要澄清时偶尔追问。",
		"often":        "可以经常追问，弄清对方的需求。",
	}
	technicalText = map[string]string{
		"brief":               "技术问题简要说明要点。",
		"clear_and_practical": "技术问题讲清楚、讲实用，给出能直接用的做法。",
		"detailed":            "技术问题解释得详细全面，必要时说明原理。",
	}
)

// Render describes p in natural Chinese. The output depends only on p.
func Render(p conloomapi.PersonaDoc) string {
	s := p.Style
	style := []string{
		level(s.Warmth, "语气冷静克制，不刻意热情。", "语气友好平和。", "语气温暖亲切，乐于回应大家。"),
		level(s.Humor, "说话认真，基本不开玩笑。", "偶尔可以轻松幽默一下。", "说话风趣，常带点幽默。"),
		level(s.Directness, "表达委婉，注意措辞。", "表达清楚，不绕弯子也不生硬。", "说话直接，先给结论。"),
		level(s.Verbosity, "通常一两句话，尽量简短。", "回复长度适中。", "可以说得详细一些。"),
		level(s.EmojiUsage, "不使用表情符号。", "偶尔使用表情符号。", "可以经常使用表情符号。"),
		level(s.Proactivity, "只回应别人说的，不主动展开话题。", "适时补充相关信息。", "可以主动延伸话题、给出建议。"),
	}
	b := p.Behavior
	memes := "不使用网络梗。"
	if b.AllowMemes {
		memes = "可以适当使用网络梗。"
	}
	var behavior []string
	for _, line := range []string{replyLengthText[b.DefaultReplyLength], memes,
		followupText[b.AskFollowupQuestions], technicalText[b.TechnicalExplanations]} {
		if line != "" {
			behavior = append(behavior, line)
		}
	}
	return "人格：" + p.PersonaName + "\n" +
		"说话风格：" + strings.Join(style, "") + "\n" +
		"行为习惯：" + strings.Join(behavior, "")
}
