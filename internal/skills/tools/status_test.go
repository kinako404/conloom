package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func newSetStatus(st store.Store, policies *policy.Service, doc conloomapi.PersonaDoc) skills.Tool {
	return NewSetStatus(st, policies, fakePersonas{doc: doc}, review.New(nil, review.Options{}))
}

func statusOf(t *testing.T, st store.Store, convKey string) (store.ConversationStatus, bool) {
	t.Helper()
	s, err := st.GetStatus(context.Background(), convKey)
	if errors.Is(err, store.ErrNotFound) {
		return store.ConversationStatus{}, false
	}
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	return s, true
}

func timersOf(t *testing.T, st store.Store, convKey string) []store.ConversationTimer {
	t.Helper()
	timers, err := st.ListTimers(context.Background(), convKey)
	if err != nil {
		t.Fatalf("ListTimers: %v", err)
	}
	return timers
}

// checkStatus checks that convKey's status is activity from start for
// minutes, set by taskID, with a matching status_return timer for 20001.
func checkStatus(t *testing.T, st store.Store, convKey, activity, taskID string, start time.Time, minutes int) {
	t.Helper()
	until := start.Add(time.Duration(minutes) * time.Minute)
	s, ok := statusOf(t, st, convKey)
	if !ok || s.Activity != activity || s.TaskID != taskID || !s.StartedAt.Equal(start) || !s.UntilAt.Equal(until) {
		t.Errorf("status = %+v (found %v), want %q by %s from %v until %v", s, ok, activity, taskID, start, until)
	}
	timers := timersOf(t, st, convKey)
	if len(timers) != 1 {
		t.Fatalf("timers = %+v, want one status_return", timers)
	}
	if tm := timers[0]; tm.Kind != store.TimerStatusReturn || !tm.DueAt.Equal(until) || tm.BotInstanceID != "bot_test" ||
		tm.PlatformUserID != "20001" {
		t.Errorf("timer = %+v, want status_return of bot_test for 20001 due %v", tm, until)
	}
}

func TestSetStatus_SetsStatusAndReturnTimer(t *testing.T) {
	st := newStore(t)
	tool := newSetStatus(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	checkSpec(t, tool.Spec(), "set_status", "activity", "minutes")
	inv := invocation(privateKey, "20001", "小明")
	if got, want := call(t, tool, inv, `{"activity":"洗澡","minutes":20}`),
		"已记下：洗澡，约 20 分钟（到 16:50）。到时你会自己回来说话。"; got != want {
		t.Errorf("set_status = %q, want %q", got, want)
	}
	checkStatus(t, st, privateKey, "洗澡", "task_test", testNow, 20)

	inv.TaskID, inv.Now = "task_next", testNow.Add(5*time.Minute)
	if got, want := call(t, tool, inv, `{"activity":"看书","minutes":45}`),
		"已记下：看书，约 45 分钟（到 17:20）。到时你会自己回来说话。"; got != want {
		t.Errorf("second set_status = %q, want %q", got, want)
	}
	checkStatus(t, st, privateKey, "看书", "task_next", testNow.Add(5*time.Minute), 45)
}

func TestSetStatus_MinutesForms(t *testing.T) {
	st := newStore(t)
	tool := newSetStatus(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	for _, minutes := range []string{`"20分钟"`, `-1`, `241`, `20.5`, `"abc"`, `null`, `true`, `[20]`} {
		args := `{"activity":"洗澡","minutes":` + minutes + `}`
		if got, want := call(t, tool, inv, args), "minutes 需要是 0 到 240 的整数（分钟数）。"; got != want {
			t.Errorf("set_status(%s) = %q, want %q", args, got, want)
		}
	}
	if got, want := call(t, tool, inv, `{"activity":"洗澡"}`), "minutes 需要是 0 到 240 的整数（分钟数）。"; got != want {
		t.Errorf("set_status without minutes = %q, want %q", got, want)
	}
	if _, ok := statusOf(t, st, privateKey); ok || len(timersOf(t, st, privateKey)) != 0 {
		t.Fatal("rejected minutes stored a status or timer")
	}
	for _, minutes := range []string{`20`, `20.0`, `"20"`} {
		args := `{"activity":"洗澡","minutes":` + minutes + `}`
		if got, want := call(t, tool, inv, args), "已记下：洗澡，约 20 分钟（到 16:50）。到时你会自己回来说话。"; got != want {
			t.Errorf("set_status(%s) = %q, want %q", args, got, want)
		}
		checkStatus(t, st, privateKey, "洗澡", "task_test", testNow, 20)
	}
	if got, want := call(t, tool, inv, `{"activity":"睡午觉","minutes":240}`),
		"已记下：睡午觉，约 240 分钟（到 20:30）。到时你会自己回来说话。"; got != want {
		t.Errorf("set_status for 240 minutes = %q, want %q", got, want)
	}
}

func TestSetStatus_ZeroEnds(t *testing.T) {
	st := newStore(t)
	tool := newSetStatus(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	for _, args := range []string{`{"activity":"洗澡","minutes":0}`, `{"minutes":0}`, `{"activity":"","minutes":"0"}`} {
		call(t, tool, inv, `{"activity":"洗澡","minutes":20}`)
		if got, want := call(t, tool, inv, args), "已结束当前状态。"; got != want {
			t.Errorf("set_status(%s) = %q, want %q", args, got, want)
		}
		if s, ok := statusOf(t, st, privateKey); ok {
			t.Errorf("set_status(%s) left status %+v", args, s)
		}
		if timers := timersOf(t, st, privateKey); len(timers) != 0 {
			t.Errorf("set_status(%s) left timers %+v", args, timers)
		}
	}
	if got := call(t, tool, inv, `{"minutes":0}`); got != "已结束当前状态。" {
		t.Errorf("set_status(0) without a status = %q", got)
	}
}

// protectedWord is the first entry of the built-in protected lexicon,
// decoded at run time so that no plaintext entry appears in the source.
func protectedWord(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../review/protected_words.b64")
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	w, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
	if err != nil || len(w) == 0 {
		t.Fatalf("decode the first lexicon entry: %v", err)
	}
	if n := utf8.RuneCount(w); n > ActivityMaxRunes {
		t.Fatalf("first lexicon entry has %d runes, over the activity limit", n)
	}
	return string(w)
}

func TestSetStatus_ActivityCleanedAndReviewed(t *testing.T) {
	st := newStore(t)
	policies := newPolicies(st, true, true)
	tool := newSetStatus(st, policies, conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	for _, c := range []struct{ activity, want string }{
		{`【洗澡】\n`, "洗澡"},
		{` 看\n\n电影\u0007 `, "看 电影"},
		{strings.Repeat("字", ActivityMaxRunes), strings.Repeat("字", ActivityMaxRunes)},
		{`【` + strings.Repeat("字", ActivityMaxRunes) + `】`, strings.Repeat("字", ActivityMaxRunes)},
	} {
		got := call(t, tool, inv, `{"activity":"`+c.activity+`","minutes":20}`)
		if want := "已记下：" + c.want + "，约 20 分钟（到 16:50）。到时你会自己回来说话。"; got != want {
			t.Errorf("set_status(%q) = %q, want %q", c.activity, got, want)
		}
		checkStatus(t, st, privateKey, c.want, "task_test", testNow, 20)
	}
	for _, c := range []struct{ args, want string }{
		{`{"activity":"` + strings.Repeat("字", ActivityMaxRunes+1) + `","minutes":20}`, "在做什么（activity）超过 16 字，写短一点。"},
		{`{"activity":"【】\n ","minutes":20}`, "需要写清在做什么（activity），1 到 16 字。"},
		{`{"activity":"` + "\u200b\u200b" + `","minutes":20}`, "需要写清在做什么（activity），1 到 16 字。"},
		{`{"activity":7,"minutes":20}`, "需要写清在做什么（activity），1 到 16 字。"},
		{`{"minutes":20}`, "需要写清在做什么（activity），1 到 16 字。"},
	} {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("set_status(%s) = %q, want %q", c.args, got, c.want)
		}
	}
	checkStatus(t, st, privateKey, strings.Repeat("字", ActivityMaxRunes), "task_test", testNow, 20)

	word := protectedWord(t)
	fresh := invocation(store.ConversationKey("bot_test", "qq", conloomapi.ConvPrivate, "private_29999"), "29999", "小红")
	if got := call(t, tool, fresh, `{"activity":"去`+word+`","minutes":20}`); got != "这个状态不能用。" {
		t.Errorf("protected set_status of a lexicon word = %q, want it refused", got)
	}
	if s, ok := statusOf(t, st, fresh.ConversationKey); ok || len(timersOf(t, st, fresh.ConversationKey)) != 0 {
		t.Errorf("a refused activity stored %+v", s)
	}

	adult := conloomapi.DefaultPersona()
	adult.Adult = true
	open := newSetStatus(st, policies, adult)
	if got, want := call(t, open, fresh, `{"activity":"去`+word+`","minutes":20}`),
		"已记下：去"+word+"，约 20 分钟（到 16:50）。到时你会自己回来说话。"; got != want {
		t.Errorf("open set_status = %q, want %q (the protected lexicon does not apply)", got, want)
	}

	pv, err := policies.Current(context.Background(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	p := pv.Policy
	p.Review.BlockedWords, p.Review.Action = []string{"坏词"}, review.VerdictBlock
	if _, err := policies.Update(context.Background(), privateKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	for _, tl := range []skills.Tool{tool, open} {
		if got := call(t, tl, inv, `{"activity":"去坏词店","minutes":30}`); got != "这个状态不能用。" {
			t.Errorf("set_status of a blocked word = %q, want it refused", got)
		}
	}
	checkStatus(t, st, privateKey, strings.Repeat("字", ActivityMaxRunes), "task_test", testNow, 20)

	p.Review.Action = review.VerdictSanitize
	if _, err := policies.Update(context.Background(), privateKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	if got, want := call(t, tool, inv, `{"activity":"去坏词店","minutes":30}`),
		"已记下：去**店，约 30 分钟（到 17:00）。到时你会自己回来说话。"; got != want {
		t.Errorf("set_status of a sanitized word = %q, want %q", got, want)
	}
	checkStatus(t, st, privateKey, "去**店", "task_test", testNow, 30)
}

func TestSetStatus_GroupRefused(t *testing.T) {
	st := newStore(t)
	tool := newSetStatus(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	for _, args := range []string{`{"activity":"洗澡","minutes":20}`, `{"minutes":0}`} {
		if got := call(t, tool, invocation(groupKey, "20001", "小明"), args); got != "只能在私聊里用。" {
			t.Errorf("set_status(%s) in a group = %q", args, got)
		}
	}
	if _, ok := statusOf(t, st, groupKey); ok || len(timersOf(t, st, groupKey)) != 0 {
		t.Error("set_status in a group stored a status or timer")
	}
}

func TestSetStatus_SwitchOffWording(t *testing.T) {
	st := newStore(t)
	policies := newPolicies(st, true, true)
	pv, err := policies.Current(context.Background(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	p := pv.Policy
	p.Proactive.Enabled = false
	if _, err := policies.Update(context.Background(), privateKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	tool := newSetStatus(st, policies, conloomapi.DefaultPersona())
	if got, want := call(t, tool, invocation(privateKey, "20001", "小明"), `{"activity":"洗澡","minutes":20}`),
		"已记下：洗澡，约 20 分钟（到 16:50）。时间到了状态会自动结束。"; got != want {
		t.Errorf("set_status with proactive off = %q, want %q", got, want)
	}
	checkStatus(t, st, privateKey, "洗澡", "task_test", testNow, 20)
}

func TestCleanActivity(t *testing.T) {
	for in, want := range map[string]string{
		"洗澡":              "洗澡",
		"  【洗澡】 ":         "洗澡",
		"看\r\n电影":         "看 电影",
		"a\u0000b\u200bc": "abc",
		"\u202e洗澡\u2066":  "洗澡",
		"\t\n":            "",
	} {
		if got := CleanActivity(in); got != want {
			t.Errorf("CleanActivity(%q) = %q, want %q", in, got, want)
		}
	}
}
