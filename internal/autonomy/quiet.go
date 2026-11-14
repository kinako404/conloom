package autonomy

import (
	"fmt"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// minInferMessages and minInferDays are how much of the user's history
	// InferQuiet needs before it trusts the hourly distribution.
	minInferMessages = 40
	minInferDays     = 5
	// quietHours is the length of an inferred quiet window.
	quietHours = 8
)

// Quiet is a daily window, in local minutes of the day, in which the bot
// starts no chat: [Start, End), wrapping midnight when Start > End. Inferred
// marks a window computed from the user's messages rather than set in the
// policy. Start == End is never produced and contains nothing.
type Quiet struct {
	Start, End int
	Inferred   bool
}

// DefaultQuiet is 00:30–08:00, used when quiet hours are neither set nor
// inferable.
var DefaultQuiet = Quiet{Start: 30, End: 480}

// Contains reports whether t falls in q in loc, at minute precision.
func (q Quiet) Contains(t time.Time, loc *time.Location) bool {
	l := t.In(loc)
	m := l.Hour()*60 + l.Minute()
	if q.Start <= q.End {
		return m >= q.Start && m < q.End
	}
	return m >= q.Start || m < q.End
}

// String formats q as "00:30–08:00".
func (q Quiet) String() string {
	return fmt.Sprintf("%02d:%02d–%02d:%02d", q.Start/60, q.Start%60, q.End/60, q.End%60)
}

// ResolveQuiet returns the quiet hours set in p, or else those InferQuiet
// finds in the user's message times.
func ResolveQuiet(p conloomapi.PrivateProactivePolicy, times []time.Time, loc *time.Location) Quiet {
	if start, end, set := p.QuietMinutes(); set && start != end {
		return Quiet{Start: start, End: end}
	}
	return InferQuiet(times, loc)
}

// EnoughData reports whether the user's message times are enough to infer
// their daily rhythm: at least 40 messages over at least 5 local days.
func EnoughData(times []time.Time, loc *time.Location) bool {
	if len(times) < minInferMessages {
		return false
	}
	days := map[string]struct{}{}
	for _, t := range times {
		days[t.In(loc).Format(time.DateOnly)] = struct{}{}
		if len(days) >= minInferDays {
			return true
		}
	}
	return false
}

// InferQuiet returns the 8 whole local hours, wrapping midnight, in which the
// user sent the fewest messages; on a tie the window starting closest to
// 00:00 around the clock, the earlier hour when two are equally close.
// Without EnoughData it returns DefaultQuiet.
func InferQuiet(times []time.Time, loc *time.Location) Quiet {
	if !EnoughData(times, loc) {
		return DefaultQuiet
	}
	h := HistOf(times, loc)
	best, bestCount, bestDist := 0, -1, 0
	for start := range 24 {
		count := 0
		for i := range quietHours {
			count += h[(start+i)%24]
		}
		dist := min(start, 24-start)
		if bestCount < 0 || count < bestCount || (count == bestCount && dist < bestDist) {
			best, bestCount, bestDist = start, count, dist
		}
	}
	return Quiet{Start: best * 60, End: (best + quietHours) % 24 * 60, Inferred: true}
}

// Hist counts messages per local hour of the day.
type Hist [24]int

// HistOf counts times per hour of the day in loc.
func HistOf(times []time.Time, loc *time.Location) Hist {
	var h Hist
	for _, t := range times {
		h[t.In(loc).Hour()]++
	}
	return h
}
