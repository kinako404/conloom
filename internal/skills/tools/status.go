package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// StatusMaxMinutes is the longest status set_status records, in minutes.
	StatusMaxMinutes = 240
	// ActivityMaxRunes is the longest activity, after CleanActivity.
	ActivityMaxRunes = 16
)

var (
	statusBadMinutes   = "minutes 需要是 0 到 " + strconv.Itoa(StatusMaxMinutes) + " 的整数（分钟数）。"
	statusNoActivity   = "需要写清在做什么（activity），1 到 " + strconv.Itoa(ActivityMaxRunes) + " 字。"
	statusLongActivity = "在做什么（activity）超过 " + strconv.Itoa(ActivityMaxRunes) + " 字，写短一点。"
)

const (
	statusRefused    = "这个状态不能用。"
	statusEnded      = "已结束当前状态。"
	statusWillReturn = "到时你会自己回来说话。"
	statusWillExpire = "时间到了状态会自动结束。"
)

// CleanActivity flattens s to one trimmed line, as the hidden state block
// shows it: runs of white space, line breaks included, become one space;
// other control characters, format characters (zero-width and bidi controls)
// and 【】 are dropped.
func CleanActivity(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '【' || r == '】':
			return -1
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

type setStatusTool struct {
	st       StatusStore
	policies PolicySource
	personas PersonaSource
	reviewer Reviewer
}

// NewSetStatus returns the set_status tool. In a private chat it records what
// the bot is doing (CleanActivity'd, 1 to ActivityMaxRunes runes, through
// OutputProtected unless the persona's content mode is open, then Output)
// from now for 1 to StatusMaxMinutes minutes, as the status of the chat's
// memory scope (memory.ScopeKey, shared by a bot's panel chats) set in the
// chat, replacing any earlier status, together with a status_return timer of
// the chat due when it ends; 0 minutes ends the scope's current status and
// cancels its timer, wherever it was set. Its answer says until when, and
// that the bot will come back then, or, with proactive.enabled off, that the
// status will simply end.
func NewSetStatus(st StatusStore, pol PolicySource, personas PersonaSource, rv Reviewer) skills.Tool {
	return &setStatusTool{st: st, policies: pol, personas: personas, reviewer: rv}
}

// MaxResultRunes declares that set_status answers with one sentence.
func (*setStatusTool) MaxResultRunes() int { return shortResultRunes }

func (*setStatusTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "set_status",
		Description: "记下你接下来要去做的一件需要一段时间的事和大约多少分钟，比如去洗澡 20 分钟；时间到了你会自己回来说话。" +
			"对方叫你提前回来时，用 minutes=0 结束当前状态。只在私聊里用，一两分钟的小事不用设。",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"activity":{"type":"string","description":"在做什么，1 到 16 字，例如“洗澡”","minLength":1,"maxLength":16},` +
			`"minutes":{"type":"integer","description":"大约多少分钟，1 到 240；0 表示结束当前状态","minimum":0,"maximum":240}},` +
			`"required":["activity","minutes"]}`),
	}
}

func (t *setStatusTool) Call(ctx context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	if inv.ConversationType != conloomapi.ConvPrivate {
		return privateOnly, nil
	}
	minutes, ok := intArg(args, "minutes")
	if !ok || minutes < 0 || minutes > StatusMaxMinutes {
		return statusBadMinutes, nil
	}
	scope := memory.ScopeKeyOf(inv.ConversationKey)
	if minutes == 0 {
		if _, err := t.st.ClearStatus(ctx, scope, time.Time{}); err != nil {
			return "", err
		}
		return statusEnded, nil
	}
	raw, _ := stringArg(args, "activity")
	activity := CleanActivity(raw)
	switch n := utf8.RuneCountInString(activity); {
	case n == 0:
		return statusNoActivity, nil
	case n > ActivityMaxRunes:
		return statusLongActivity, nil
	}
	pv, err := t.policies.Current(ctx, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	doc, err := t.personas.Persona(ctx, inv.BotInstanceID, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	check := t.reviewer.OutputProtected
	if conloomapi.ModeFor(doc, inv.ConversationType) == conloomapi.ContentOpen {
		check = t.reviewer.Output
	}
	res := check(activity, pv.Policy.Review)
	if activity = CleanActivity(res.Text); res.Verdict == review.VerdictBlock || activity == "" {
		return statusRefused, nil
	}
	now, loc := clock(inv)
	until := now.Add(time.Duration(minutes) * time.Minute)
	if err := t.st.SetStatus(ctx,
		store.ConversationStatus{ConversationKey: scope, OriginConversationKey: inv.ConversationKey, Activity: activity,
			TaskID: inv.TaskID, StartedAt: now, UntilAt: until},
		&store.ConversationTimer{ConversationKey: inv.ConversationKey, Kind: store.TimerStatusReturn,
			BotInstanceID: inv.BotInstanceID, PlatformUserID: inv.PlatformUserID, DueAt: until}); err != nil {
		return "", err
	}
	then := statusWillReturn
	if !pv.Policy.Proactive.Enabled {
		then = statusWillExpire
	}
	return "已记下：" + activity + "，约 " + strconv.Itoa(minutes) + " 分钟（到 " + until.In(loc).Format("15:04") + "）。" + then, nil
}
