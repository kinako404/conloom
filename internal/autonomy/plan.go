package autonomy

import (
	"math/rand/v2"
	"time"
)

const (
	// planStep and planHorizon space the candidate times PlanInitiate weighs.
	planStep    = 15 * time.Minute
	planHorizon = 24 * time.Hour
	// planJitterMinutes bounds the random minutes added to the picked candidate.
	planJitterMinutes = 15
	// followUpMin and followUpSpread place a follow-up 1.5–4 hours after the initiate.
	followUpMin    = 90 * time.Minute
	followUpSpread = 150 * time.Minute
	// quietEndJitterMinutes bounds the random minutes after quiet hours end.
	quietEndJitterMinutes = 60
)

// PlanInput is what PlanInitiate needs. Last is the chat's newest message
// from either side (zero when none); Level is an effective level (see
// DailyCap); Hist and Enough describe the user's messages (see HistOf and
// EnoughData); SentOn returns how many messages the bot started on a local
// day given as "2006-01-02" (time.DateOnly) in Loc, a nil SentOn counts none.
// Loc and Rand are required.
type PlanInput struct {
	Now, Last time.Time
	Level     string
	Quiet     Quiet
	Hist      Hist
	Enough    bool
	SentOn    func(day string) int
	Loc       *time.Location
	Rand      *rand.Rand
}

// PlanInitiate picks when the bot next starts the chat: a time no earlier
// than Now and than Last plus MinGap, within the following day, outside the
// quiet hours and on a local day below DailyCap. Candidates every 15 minutes
// are weighted by the user's share of messages in their hour (equally without
// Enough), and the picked one gets 0–14 random minutes unless those would
// move it into quiet hours or onto a day at cap. It reports false when the
// level is off or no candidate is left.
func PlanInitiate(in PlanInput) (time.Time, bool) {
	limit := DailyCap(in.Level)
	if limit == 0 {
		return time.Time{}, false
	}
	allowed := func(t time.Time) bool {
		if in.Quiet.Contains(t, in.Loc) {
			return false
		}
		return in.SentOn == nil || in.SentOn(t.In(in.Loc).Format(time.DateOnly)) < limit
	}
	total := 0
	for _, n := range in.Hist {
		total += n
	}

	earliest := in.Now
	if next := in.Last.Add(MinGap(in.Level)); next.After(earliest) {
		earliest = next
	}
	var candidates []time.Time
	var weights []float64
	sum := 0.0
	for t := earliest; t.Before(earliest.Add(planHorizon)); t = t.Add(planStep) {
		if !allowed(t) {
			continue
		}
		w := 1.0
		if in.Enough && total > 0 {
			w = 0.2 + float64(in.Hist[t.In(in.Loc).Hour()])/float64(total)*10
		}
		candidates = append(candidates, t)
		weights = append(weights, w)
		sum += w
	}
	if len(candidates) == 0 {
		return time.Time{}, false
	}

	picked := candidates[len(candidates)-1]
	x := in.Rand.Float64() * sum
	for i, w := range weights {
		if x < w {
			picked = candidates[i]
			break
		}
		x -= w
	}
	if jittered := picked.Add(time.Duration(in.Rand.IntN(planJitterMinutes)) * time.Minute); allowed(jittered) {
		return jittered, true
	}
	return picked, true
}

// PlanFollowUp picks when the bot follows up an initiate sent at now: 1.5–4
// hours later, or, when that falls in q, 0–59 minutes after q next ends.
func PlanFollowUp(now time.Time, q Quiet, loc *time.Location, r *rand.Rand) time.Time {
	t := now.Add(followUpMin + time.Duration(r.IntN(int(followUpSpread/time.Minute)+1))*time.Minute)
	if !q.Contains(t, loc) {
		return t
	}
	end := q.EndAfter(t, loc)
	if jittered := end.Add(time.Duration(r.IntN(quietEndJitterMinutes)) * time.Minute); !q.Contains(jittered, loc) {
		return jittered
	}
	return end
}
