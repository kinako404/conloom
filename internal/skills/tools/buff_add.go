package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// MaxCustomBars bounds the bars a bot adds itself to a memory scope.
	MaxCustomBars = 6
	// MaxEffects bounds the effects active at once in a memory scope.
	MaxEffects = 6
	// EffectMinMinutes and EffectMaxMinutes bound how long an effect lasts.
	EffectMinMinutes = 5
	EffectMaxMinutes = 7 * 24 * 60
	// defaultBuffHalfLife is the half-life of an added bar that gives none.
	defaultBuffHalfLife = 6 * time.Hour
)

// The kinds add_buff adds.
const (
	buffKindBar    = "bar"
	buffKindEffect = "effect"
)

var (
	addBuffBadName = "名字（name）需要 1 到 " + strconv.Itoa(conloomapi.MaxBarNameRunes) +
		" 个字，不能有空白、冒号、【】或看不见的字符。"
	addBuffLongDescription = "说明（description）超过 " + strconv.Itoa(conloomapi.MaxBarDescriptionRunes) + " 字，写短一点。"
	addBuffBadHalfLife     = "half_life_hours（回落的半衰期）需要是 " + hoursText(conloomapi.MinBarHalfLife) + " 到 " +
		hoursText(conloomapi.MaxBarHalfLife) + " 之间的小时数。"
	addBuffBadMinutes = "minutes 需要是 " + strconv.Itoa(EffectMinMinutes) + " 到 " + strconv.Itoa(EffectMaxMinutes) +
		" 的整数（分钟数，最多 7 天）。"
)

const (
	addBuffBadKind            = "kind 需要是 bar（数值条）或 effect（效果）。"
	addBuffBadBaseline        = "baseline（平常水平）需要是 0 到 100 的整数。"
	addBuffBadValue           = "value（现在的值）需要是 0 到 100 的整数。"
	addBuffNameRefused        = "这个名字不能用。"
	addBuffDescriptionRefused = "这个说明不能用。"
)

// hoursText formats d in hours as "0.5" or "168".
func hoursText(d time.Duration) string {
	return strconv.FormatFloat(d.Hours(), 'f', -1, 64)
}

type addBuffTool struct {
	st       BuffStore
	policies PolicySource
	personas PersonaSource
	reviewer Reviewer
}

// NewAddBuff returns the add_buff tool. In a private chat it adds, to the
// chat's memory scope (memory.ScopeKey, shared by a bot's panel chats),
// either a hidden bar (kind bar: a baseline of 0–100, a half-life of
// 0.5–168 hours, by default 6, and a value of 0–100, by default the
// baseline), kept and adjusted like the persona's, or an effect (kind
// effect) that runs out after EffectMinMinutes to EffectMaxMinutes minutes.
// The name follows the persona's bar name rules (validBuffName) and must be
// neither one of the persona's bars nor held by a bar the bot added or an
// effect still active; the description, CleanActivity'd, has at most
// conloomapi.MaxBarDescriptionRunes runes. Both go through OutputProtected
// unless the persona's content mode is open, then Output: a blocked name or
// description, or a name review would change, is refused, a sanitized
// description kept as sanitized. A scope holds at most MaxCustomBars bars and
// MaxEffects active effects. A task adding the same kind and name again
// changes nothing and is answered as the first time, so a re-run adds it only
// once. The answer names a bar's band or an effect's time left, never a
// value.
func NewAddBuff(st BuffStore, pol PolicySource, personas PersonaSource, rv Reviewer) skills.Tool {
	return &addBuffTool{st: st, policies: pol, personas: personas, reviewer: rv}
}

// MaxResultRunes declares that add_buff answers with one sentence.
func (*addBuffTool) MaxResultRunes() int { return shortResultRunes }

func (*addBuffTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "add_buff",
		Description: "给自己加一条只有你自己知道的状态。kind=bar 是数值条，会慢慢回到平常水平，用来长期跟踪饥饿、困意这类东西，" +
			"加上后用 adjust_buff 调；kind=effect 是一段时间的临时状况，比如着凉、刚吃饱，到时间自动消失。种类保持少量，不要向对方提起。",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"kind":{"type":"string","enum":["bar","effect"],"description":"bar 是数值条，effect 是效果"},` +
			`"name":{"type":"string","description":"名字，1 到 6 字，不能有空白、冒号、【】，例如“饥饿”“着凉”","minLength":1,"maxLength":6},` +
			`"description":{"type":"string","description":"一句说明，最多 40 字；数值条写清高低各是什么意思，例如“高=很饿，低=很饱”","maxLength":40},` +
			`"baseline":{"type":"integer","description":"kind=bar 时必填：平常水平，0 到 100","minimum":0,"maximum":100},` +
			`"half_life_hours":{"type":"number","description":"kind=bar 时：回到平常水平的快慢，半衰期 0.5 到 168 小时，默认 6","minimum":0.5,"maximum":168},` +
			`"value":{"type":"integer","description":"kind=bar 时：现在的值，0 到 100，默认等于平常水平","minimum":0,"maximum":100},` +
			`"minutes":{"type":"integer","description":"kind=effect 时必填：持续多少分钟，5 到 10080（7 天）","minimum":5,"maximum":10080}},` +
			`"required":["kind","name"]}`),
	}
}

func (t *addBuffTool) Call(ctx context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	if inv.ConversationType != conloomapi.ConvPrivate {
		return privateOnly, nil
	}
	kind, _ := stringArg(args, "kind")
	if kind = strings.TrimSpace(kind); kind != buffKindBar && kind != buffKindEffect {
		return addBuffBadKind, nil
	}
	name, _ := stringArg(args, "name")
	if name = strings.TrimSpace(name); !validBuffName(name) {
		return addBuffBadName, nil
	}
	raw, _ := stringArg(args, "description")
	description := CleanActivity(raw)
	if utf8.RuneCountInString(description) > conloomapi.MaxBarDescriptionRunes {
		return addBuffLongDescription, nil
	}
	var bar store.CustomBar
	var value, minutes int
	if kind == buffKindBar {
		var problem string
		if bar, value, problem = barArgs(args); problem != "" {
			return problem, nil
		}
	} else {
		var ok bool
		if minutes, ok = intArg(args, "minutes"); !ok || minutes < EffectMinMinutes || minutes > EffectMaxMinutes {
			return addBuffBadMinutes, nil
		}
	}
	doc, err := t.personas.Persona(ctx, inv.BotInstanceID, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	if isPersonaBar(doc, name) {
		return "“" + name + "”是固定的状态条，换个名字。", nil
	}
	pv, err := t.policies.Current(ctx, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	check := t.reviewer.OutputProtected
	if conloomapi.ModeFor(doc, inv.ConversationType) == conloomapi.ContentOpen {
		check = t.reviewer.Output
	}
	if res := check(name, pv.Policy.Review); res.Verdict == review.VerdictBlock || res.Text != name {
		return addBuffNameRefused, nil
	}
	if description != "" {
		res := check(description, pv.Policy.Review)
		if res.Verdict == review.VerdictBlock {
			return addBuffDescriptionRefused, nil
		}
		description = CleanActivity(res.Text)
	}

	now, _ := clock(inv)
	scope := memory.ScopeKeyOf(inv.ConversationKey)
	var until time.Time
	if kind == buffKindBar {
		bar.ConversationKey, bar.Name, bar.Description, bar.TaskID, bar.CreatedAt = scope, name, description, inv.TaskID, now
		err = t.st.AddCustomBar(ctx, bar, float64(value), MaxCustomBars)
	} else {
		until = now.Add(time.Duration(minutes) * time.Minute)
		err = t.st.AddEffect(ctx, store.Effect{ConversationKey: scope, Name: name, Description: description, TaskID: inv.TaskID,
			StartedAt: now, UntilAt: until}, MaxEffects, now)
	}
	switch {
	case errors.Is(err, store.ErrBuffNameTaken):
		return "已经有叫“" + name + "”的状态条或效果了，换个名字。", nil
	case errors.Is(err, store.ErrBuffLimit):
		return t.full(ctx, kind, scope, now)
	case err != nil:
		return "", err
	case kind == buffKindBar:
		return "已加上状态条：" + name + "，现在" + autonomy.Band(float64(value)) + "。", nil
	}
	return "已加上效果：" + name + "，" + autonomy.TimeLeft(until.Sub(now)) + "。", nil
}

// barArgs reads the bar of an add_buff of kind bar from args: its baseline
// and half-life, and its value. problem is the answer to arguments out of
// range.
func barArgs(args json.RawMessage) (bar store.CustomBar, value int, problem string) {
	baseline, ok := intArg(args, "baseline")
	if !ok || baseline < 0 || baseline > 100 {
		return bar, 0, addBuffBadBaseline
	}
	halfLife := defaultBuffHalfLife
	if hasArg(args, "half_life_hours") {
		hours, ok := numberArg(args, "half_life_hours")
		if !ok || hours < conloomapi.MinBarHalfLife.Hours() || hours > conloomapi.MaxBarHalfLife.Hours() {
			return bar, 0, addBuffBadHalfLife
		}
		halfLife = time.Duration(math.Round(hours*3600)) * time.Second
	}
	value = baseline
	if hasArg(args, "value") {
		if value, ok = intArg(args, "value"); !ok || value < 0 || value > 100 {
			return bar, 0, addBuffBadValue
		}
	}
	return store.CustomBar{Baseline: baseline, HalfLife: halfLife}, value, ""
}

// full answers an add_buff of kind that the scope's limit refused, naming
// the bars, or the effects active at now, that fill it.
func (t *addBuffTool) full(ctx context.Context, kind, scope string, now time.Time) (string, error) {
	var names []string
	if kind == buffKindBar {
		bars, err := t.st.ListCustomBars(ctx, scope)
		if err != nil {
			return "", err
		}
		for _, b := range bars {
			names = append(names, b.Name)
		}
		return "自建的状态条已经满了：" + strings.Join(names, "、") + "。先用 remove_buff 删掉不需要的。", nil
	}
	effects, err := t.st.ListEffects(ctx, scope, now)
	if err != nil {
		return "", err
	}
	for _, e := range effects {
		names = append(names, e.Name)
	}
	return "生效中的效果已经满了：" + strings.Join(names, "、") + "。先用 remove_buff 去掉不需要的。", nil
}

// validBuffName reports whether name may name a bar or effect the bot adds:
// it follows the persona's bar name rules (conloomapi.ValidBarName); as the
// hidden state block shows names as they are, has no format characters such
// as bidi controls either; and is not made of dots only, a path segment the
// panel's and the API's DELETE …/{name} would never receive.
func validBuffName(name string) bool {
	return conloomapi.ValidBarName(name) && !strings.ContainsFunc(name, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) &&
		strings.Trim(name, ".") != ""
}
