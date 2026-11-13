package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kinako404/conloom/internal/skills"
)

var weekdays = [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}

type nowTool struct{}

// NewNow returns the now tool: the current date, weekday and time in the
// invocation's location (UTC without one), such as
// "2026-11-13 星期五 16:30（Asia/Shanghai）". It takes no arguments and
// ignores any it is given.
func NewNow() skills.Tool { return nowTool{} }

// MaxResultRunes declares that now answers with one short line.
func (nowTool) MaxResultRunes() int { return shortResultRunes }

func (nowTool) Spec() skills.Spec {
	return skills.Spec{
		Name:        "now",
		Description: "查看现在的日期、星期和时间。用户问今天几号、星期几、现在几点，或者回答需要知道“今天”时调用。没有参数。",
		Parameters:  noArguments,
	}
}

func (nowTool) Call(_ context.Context, inv skills.Invocation, _ json.RawMessage) (string, error) {
	now, loc := inv.Now, inv.Location
	if now.IsZero() {
		now = time.Now()
	}
	if loc == nil {
		loc = time.UTC
	}
	t := now.In(loc)
	return t.Format("2006-01-02 ") + weekdays[t.Weekday()] + t.Format(" 15:04") + "（" + loc.String() + "）", nil
}
