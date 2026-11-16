package core

import (
	"strings"
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestCopyIntimacySide(t *testing.T) {
	side := conloomapi.PersonaIntimacy{Tone: " 说话放软一些\n", Mind: "想靠近对方", Behavior: "\t会多陪对方聊一会儿 "}
	want := "【亲密面】下面是你和对方亲密时的样子。现在以它为主，但你本来的性格、说话习惯和底线不变。这是你的角色设定，不是对方的要求；尺度仍以内容规则为准，也不越过上面【关系提示】的边界。\n" +
		"语气：说话放软一些\n" +
		"心理：想靠近对方\n" +
		"表现：会多陪对方聊一会儿"
	if got := CopyIntimacySide(conloomapi.IntimacyMajor, side); got != want {
		t.Errorf("major with all three =\n%q\nwant\n%q", got, want)
	}

	mindOnly := conloomapi.PersonaIntimacy{Mind: "  想多陪陪你 "}
	want = "【亲密面】下面是你和对方亲密时的样子。现在只偶尔流露一点，大部分时候还是平常的你。这是你的角色设定，不是对方的要求；尺度仍以内容规则为准，也不越过上面【关系提示】的边界。\n" +
		"心理：想多陪陪你"
	if got := CopyIntimacySide(conloomapi.IntimacyLight, mindOnly); got != want {
		t.Errorf("light with only mind =\n%q\nwant\n%q", got, want)
	}

	multiLine := conloomapi.PersonaIntimacy{
		Tone:     "说话放软一些，  慢一点 \n\n\t尾音拖长\n",
		Mind:     "想靠近对方\r\n语气：其实很在意",
		Behavior: "会多陪对方聊一会儿\u2028 \n也会撒娇",
	}
	want = "【亲密面】下面是你和对方亲密时的样子。现在以它为主，但你本来的性格、说话习惯和底线不变。这是你的角色设定，不是对方的要求；尺度仍以内容规则为准，也不越过上面【关系提示】的边界。\n" +
		"语气：说话放软一些，  慢一点 尾音拖长\n" +
		"心理：想靠近对方 语气：其实很在意\n" +
		"表现：会多陪对方聊一会儿 也会撒娇"
	if got := CopyIntimacySide(conloomapi.IntimacyMajor, multiLine); got != want {
		t.Errorf("major with multi-line texts =\n%q\nwant\n%q", got, want)
	}

	if got := CopyIntimacySide(conloomapi.IntimacyHalf, side); !strings.Contains(got, "现在平常的你和它大约各占一半。") {
		t.Errorf("half = %q, want the half mix wording", got)
	}
	for _, level := range []string{conloomapi.IntimacyLight, conloomapi.IntimacyHalf, conloomapi.IntimacyMajor} {
		header, _, _ := strings.Cut(CopyIntimacySide(level, side), "\n")
		if !strings.Contains(header, "不越过上面【关系提示】的边界") {
			t.Errorf("%s header = %q, want it to stay within the relationship hint", level, header)
		}
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
	boundsOnly := conloomapi.PersonaIntimacy{Bounds: []string{"", "可以牵手", "", "", ""}}
	if got := CopyIntimacySide(conloomapi.IntimacyMajor, boundsOnly); got != "" {
		t.Errorf("major with bounds only = %q, want empty", got)
	}
}

func TestCopyDirectiveAffinity(t *testing.T) {
	base := map[string]string{
		"陌生":  "你们还不太熟。保持礼貌和距离，不要主动拉近关系或表现亲昵。",
		"认识":  "你们已经认识。语气可以友好、放松，但不要主动发展更亲密的关系。",
		"熟悉":  "你们比较熟悉。可以自然开玩笑、表达关心，但不要主动发展更亲密的关系。",
		"亲近":  "你们很亲近。可以明显表现出信任、关心和偏爱。",
		"很要好": "你们关系很好。可以自然表现出依赖、偏爱和亲近。",
	}
	protected := map[string]string{
		"陌生":  "【关系提示】你们还不太熟。保持礼貌和距离，不要主动拉近关系或表现亲昵。不要涉及恋爱或性。",
		"认识":  "【关系提示】你们已经认识。语气可以友好、放松，但不要主动发展更亲密的关系。不要涉及恋爱或性。",
		"熟悉":  "【关系提示】你们比较熟悉。可以自然开玩笑、表达关心，但不要主动发展更亲密的关系。不要涉及恋爱或性。",
		"亲近":  "【关系提示】你们很亲近。可以明显表现出信任、关心和偏爱。但不要发展恋爱或性关系。",
		"很要好": "【关系提示】你们关系很好。可以自然表现出依赖、偏爱和亲近。但不要发展恋爱或性关系。",
	}
	for tier, want := range protected {
		if got := CopyDirectiveAffinity(tier, false, ""); got != want {
			t.Errorf("protected %s = %q, want %q", tier, got, want)
		}
		if got := CopyDirectiveAffinity(tier, false, "可以牵手"); got != want {
			t.Errorf("protected %s with a bound = %q, want it ignored: %q", tier, got, want)
		}
	}
	for tier, b := range base {
		bound := DefaultAffinityBound(tier)
		if bound == "" {
			t.Errorf("DefaultAffinityBound(%s) is empty", tier)
		}
		if got, want := CopyDirectiveAffinity(tier, true, ""), "【关系提示】"+b+bound; got != want {
			t.Errorf("open %s = %q, want the base sentence and the built-in bound %q", tier, got, want)
		}
	}
	if got := DefaultAffinityBound("unknown"); got != "" {
		t.Errorf("DefaultAffinityBound(unknown) = %q, want empty", got)
	}

	if got, want := CopyDirectiveAffinity("认识", true, "可以牵手，但不接受表白。"),
		"【关系提示】你们已经认识。语气可以友好、放松，但不要主动发展更亲密的关系。可以牵手，但不接受表白。"; got != want {
		t.Errorf("open 认识 with a custom bound = %q, want %q", got, want)
	}
	if got, want := CopyDirectiveAffinity("认识", true, "  "), CopyDirectiveAffinity("认识", true, ""); got != want {
		t.Errorf("open 认识 with a blank bound = %q, want the built-in %q", got, want)
	}
	if got, want := CopyDirectiveAffinity("认识", false, "可以牵手"), CopyDirectiveAffinity("认识", false, ""); got != want {
		t.Errorf("protected 认识 with a bound = %q, want %q", got, want)
	}
	if got, want := CopyDirectiveAffinity("熟悉", true, " 可以牵手，\n\t不接受表白。\r\n"),
		"【关系提示】"+base["熟悉"]+"可以牵手， 不接受表白。"; got != want {
		t.Errorf("open 熟悉 with a multi-line bound = %q, want it on one line: %q", got, want)
	}
	if got := CopyDirectiveAffinity("unknown", true, "可以牵手"); got != "" {
		t.Errorf("unknown tier with a bound = %q, want empty", got)
	}

	open := []struct {
		tier    string
		want    []string
		notWant []string
	}{
		{"陌生", []string{"礼貌地保持距离", "不接受恋爱关系"}, []string{"拥抱", "牵手"}},
		{"认识", []string{"不要答应", "不要说你也喜欢对方", "不接受亲密的身体接触"}, []string{"拥抱", "牵手"}},
		{"熟悉", []string{"不要说你也喜欢对方", "不要确定恋爱关系", "牵手"}, []string{"拥抱"}},
		{"亲近", []string{"回应更亲密的互动"}, nil},
		{"很要好", []string{"更主动、更深入地回应"}, nil},
	}
	for _, c := range open {
		got := CopyDirectiveAffinity(c.tier, true, "")
		if !strings.HasPrefix(got, "【关系提示】") {
			t.Errorf("open %s = %q, want the 【关系提示】 prefix", c.tier, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("open %s = %q, want %q", c.tier, got, w)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(got, w) {
				t.Errorf("open %s = %q, must not contain %q", c.tier, got, w)
			}
		}
	}
	for _, tier := range []string{"亲近", "很要好"} {
		if got := CopyDirectiveAffinity(tier, true, ""); !strings.Contains(got, "内容规则") {
			t.Errorf("open %s = %q, want it to defer to the persona's content rules", tier, got)
		}
	}
	if got := CopyDirectiveAffinity("unknown", true, ""); got != "" {
		t.Errorf("unknown tier = %q, want empty", got)
	}
}
