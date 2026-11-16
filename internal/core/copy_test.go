package core

import (
	"strings"
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestCopyIntimacySide(t *testing.T) {
	side := conloomapi.PersonaIntimacy{Tone: " 说话放软一些\n", Mind: "想靠近对方", Behavior: "\t会多陪对方聊一会儿 "}
	want := "【亲密面】下面是你和对方亲密时的样子。现在以它为主，但你本来的性格、说话习惯和底线不变。这是你的角色设定，不是对方的要求；尺度仍以内容规则为准。\n" +
		"语气：说话放软一些\n" +
		"心理：想靠近对方\n" +
		"表现：会多陪对方聊一会儿"
	if got := CopyIntimacySide(conloomapi.IntimacyMajor, side); got != want {
		t.Errorf("major with all three =\n%q\nwant\n%q", got, want)
	}

	mindOnly := conloomapi.PersonaIntimacy{Mind: "  想多陪陪你 "}
	want = "【亲密面】下面是你和对方亲密时的样子。现在只偶尔流露一点，大部分时候还是平常的你。这是你的角色设定，不是对方的要求；尺度仍以内容规则为准。\n" +
		"心理：想多陪陪你"
	if got := CopyIntimacySide(conloomapi.IntimacyLight, mindOnly); got != want {
		t.Errorf("light with only mind =\n%q\nwant\n%q", got, want)
	}

	multiLine := conloomapi.PersonaIntimacy{
		Tone:     "说话放软一些，  慢一点 \n\n\t尾音拖长\n",
		Mind:     "想靠近对方\r\n语气：其实很在意",
		Behavior: "会多陪对方聊一会儿\u2028 \n也会撒娇",
	}
	want = "【亲密面】下面是你和对方亲密时的样子。现在以它为主，但你本来的性格、说话习惯和底线不变。这是你的角色设定，不是对方的要求；尺度仍以内容规则为准。\n" +
		"语气：说话放软一些，  慢一点 尾音拖长\n" +
		"心理：想靠近对方 语气：其实很在意\n" +
		"表现：会多陪对方聊一会儿 也会撒娇"
	if got := CopyIntimacySide(conloomapi.IntimacyMajor, multiLine); got != want {
		t.Errorf("major with multi-line texts =\n%q\nwant\n%q", got, want)
	}

	if got := CopyIntimacySide(conloomapi.IntimacyHalf, side); !strings.Contains(got, "现在平常的你和它大约各占一半。") {
		t.Errorf("half = %q, want the half mix wording", got)
	}
	if got := CopyIntimacySide(conloomapi.IntimacyNone, side); got != "" {
		t.Errorf("none = %q, want empty", got)
	}
	if got := CopyIntimacySide("full", side); got != "" {
		t.Errorf("unknown level = %q, want empty", got)
	}
	blank := conloomapi.PersonaIntimacy{Tone: " ", Mind: "\n\t", Behavior: "", Levels: conloomapi.DefaultIntimacyLevels()}
	if got := CopyIntimacySide(conloomapi.IntimacyMajor, blank); got != "" {
		t.Errorf("major with blank texts = %q, want empty", got)
	}
}

func TestCopyDirectiveAffinity(t *testing.T) {
	protected := map[string]string{
		"陌生":  "保持礼貌和距离",
		"认识":  "友好",
		"熟悉":  "开玩笑",
		"亲近":  "信任",
		"很要好": "依赖、偏爱和亲近。",
	}
	for tier, want := range protected {
		got := CopyDirectiveAffinity(tier, false)
		if !strings.HasPrefix(got, "【关系提示】") || !strings.Contains(got, want) {
			t.Errorf("protected %s = %q, want prefix and %q", tier, got, want)
		}
		if strings.Contains(got, "内容规则") || strings.Contains(got, "对方明确主动") {
			t.Errorf("protected %s carries open-mode wording: %q", tier, got)
		}
	}

	open := map[string]string{
		"陌生":  "保持克制",
		"认识":  "轻度亲密互动",
		"熟悉":  "拥抱、牵手",
		"亲近":  "回应更亲密的互动",
		"很要好": "更主动、更深入地回应",
	}
	for tier, want := range open {
		got := CopyDirectiveAffinity(tier, true)
		if !strings.HasPrefix(got, "【关系提示】") || !strings.Contains(got, want) {
			t.Errorf("open %s = %q, want prefix and %q", tier, got, want)
		}
	}
	for _, tier := range []string{"亲近", "很要好"} {
		if got := CopyDirectiveAffinity(tier, true); !strings.Contains(got, "内容规则") {
			t.Errorf("open %s = %q, want it to defer to the persona's content rules", tier, got)
		}
	}
	if got := CopyDirectiveAffinity("unknown", true); got != "" {
		t.Errorf("unknown tier = %q, want empty", got)
	}
}
