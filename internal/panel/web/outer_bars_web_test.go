package web

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// The persona form's bar rows edit a bar's kind and step limits: a blank
// limit is left out, a bad one or a daily limit below the step limit is an
// error, and a saved bar keeps the fields the rows do not show while an
// untouched inner bar saves as before.
func TestPersonaForm_BarKindAndLimits(t *testing.T) {
	js := assets(t, ".js")["views/personaform.js"]
	m := regexp.MustCompile(`const BAR_KIND = \{(.*?)\};`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/personaform.js has no BAR_KIND")
	}
	var got []string
	for _, k := range regexp.MustCompile(`'?(\w*)'?: '`).FindAllStringSubmatch(m[1], -1) {
		got = append(got, k[1])
	}
	if want := []string{conloomapi.BarKindInner, conloomapi.BarKindOuter}; !slices.Equal(got, want) {
		t.Errorf("BAR_KIND keys = %q, want %q", got, want)
	}
	for _, want := range []string{
		"const BAR_KIND = { inner: '内在', outer: '外在' };",
		"const BAR_KIND_HINT = { inner: '只影响 bot 的语气和想不想多聊，bot 不会提起', outer: '别人看得出来，比如吃饱、脸红；bot 会在动作和外表描写里自然带出，不说数字' };",
		"select(BAR_KIND, bar.kind === 'outer' ? 'outer' : 'inner')",
		"'类型'", "'单次最多'", "'每天最多'", "BAR_KIND_HINT[",
		"'1–100 的整数，一次最多调多少；留空为 10'",
		"'1–100 的整数，一天里净变化最多多少，不能小于单次最多；留空为 30'",
		"placeholder: String(BAR_DEFAULT_MAX_STEP)", "placeholder: String(BAR_DEFAULT_MAX_DAY)",
		"value: bar.max_step ?? ''", "value: bar.max_day ?? ''",
		"barLimit(r.maxStep.value)", "barLimit(r.maxDay.value)",
		"delete out.kind", "delete out.max_step", "delete out.max_day", "out.kind = 'outer'",
		"`第 ${i + 1} 条状态条的单次最多必须是 1–100 的整数，或留空`",
		"`第 ${i + 1} 条状态条的每天最多必须是 1–100 的整数，或留空`",
		"`第 ${i + 1} 条状态条的每天最多不能小于单次最多（${step ?? BAR_DEFAULT_MAX_STEP}）`",
		"(day ?? BAR_DEFAULT_MAX_DAY) < (step ?? BAR_DEFAULT_MAX_STEP)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/personaform.js lacks %s", want)
		}
	}
	if !regexp.MustCompile(`const out = \{\s*\.\.\.r\.bar,\s*name: r\.name\.value\.trim\(\),\s*baseline,\s*half_life: r\.halfLife\.value\.trim\(\),\s*description: r\.description\.value\.trim\(\),\s*\};`).MatchString(js) {
		t.Error("views/personaform.js does not build a saved bar from the bar it read, keeping fields the row does not show")
	}
	if !regexp.MustCompile(`if \(step !== undefined\) out\.max_step = step;`).MatchString(js) ||
		!regexp.MustCompile(`if \(day !== undefined\) out\.max_day = day;`).MatchString(js) {
		t.Error("views/personaform.js does not save a limit only when it is set")
	}
	if !regexp.MustCompile(`if \(r\.kind\.value === 'outer'\) out\.kind = 'outer';`).MatchString(js) {
		t.Error("views/personaform.js does not save the kind only for an outer bar")
	}
	hint := regexp.MustCompile(`h\('h3', null, ` + "`" + `状态条（最多 \$\{MAX_BARS\} 条）` + "`" + `\),\s*h\('p', \{ class: 'hint' \}, '([^']*)'\)`).FindStringSubmatch(js)
	if hint == nil {
		t.Fatal("views/personaform.js has no bars hint")
	}
	if want := "私聊里 bot 自己的数值（0–100），不进系统提示；bot 启用了 buff 技能才生效。内在的只影响语气和想不想多聊，bot 不会提起；外在的是别人看得出来的状态，bot 会在动作和外表描写里自然带出，但不说数字。一条都不写时用默认的心情和精力。"; hint[1] != want {
		t.Errorf("bars hint = %q, want %q", hint[1], want)
	}
	if strings.Contains(js, "只有 bot 自己知道") {
		t.Error("views/personaform.js still says the bars are known to the bot only")
	}
	if !strings.Contains(js, "import { barLimit, BAR_DEFAULT_MAX_STEP, BAR_DEFAULT_MAX_DAY, BAR_MAX_LIMIT } from '../lib/format.js';") {
		t.Error("views/personaform.js does not import the bar limit helpers")
	}
}

func TestFormat_BarLimitsMatchPersona(t *testing.T) {
	js := assets(t, ".js")["lib/format.js"]
	for name, want := range map[string]int{
		"BAR_DEFAULT_MAX_STEP": conloomapi.DefaultBarMaxStep,
		"BAR_DEFAULT_MAX_DAY":  conloomapi.DefaultBarMaxDay,
		"BAR_MAX_LIMIT":        conloomapi.MaxBarLimit,
	} {
		m := regexp.MustCompile(`export const ` + name + ` = (\d+);`).FindStringSubmatch(js)
		if m == nil {
			t.Errorf("lib/format.js has no %s", name)
			continue
		}
		if m[1] != strconv.Itoa(want) {
			t.Errorf("%s = %s, want %d", name, m[1], want)
		}
	}
	for _, want := range []string{
		"export function barLimit(text)", "export function barLimitsText(b)",
		"/^\\d+$/", "`单次最多 ±${", "，每天最多 ±${",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("lib/format.js lacks %s", want)
		}
	}
}

func TestProfile_OuterBarTag(t *testing.T) {
	js := assets(t, ".js")["views/profile.js"]
	for _, want := range []string{
		"b.kind === 'outer'", "'外在'", "tg-profile-tag",
		"[b.name, b.kind === 'outer' ? h('span', { class: 'badge tg-profile-tag' }, '外在') : null]",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/profile.js lacks %s", want)
		}
	}
	css := assets(t, ".css")["style.css"]
	if !regexp.MustCompile(`\.chat-app \.tg-profile-tag\s*\{`).MatchString(css) {
		t.Error("style.css does not style .chat-app .tg-profile-tag")
	}
}

func TestAutonomyTab_OuterBarTag(t *testing.T) {
	js := assets(t, ".js")["views/autonomy.js"]
	for _, want := range []string{
		"b.kind === 'outer'", "'外在'", "barLimitsText(b)",
		"b.kind === 'outer' ? [' ', h('span', { class: 'badge' }, '外在')] : null",
		"barLimitsText(b) ? h('div', { class: 'muted small' }, barLimitsText(b)) : null",
		"标“外在”的是别人看得出来的状态，bot 会在动作和外表描写里带出来。",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("views/autonomy.js lacks %s", want)
		}
	}
}
