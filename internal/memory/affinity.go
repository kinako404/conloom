package memory

import (
	"time"

	"github.com/kinako404/conloom/internal/store"
)

const (
	InitialAffinity = 20
	DecayFloor      = 20
	MaxRunDelta     = 3
	DailyGainCap    = 5
	DailyLossCap    = 10
	DecayGraceDays  = 14
	DecayStepDays   = 7
	// NoteMaxRunes and SummaryMaxRunes are a group's default note and
	// summary limits (see NoteLimit and SummaryLimit).
	NoteMaxRunes    = 300
	SummaryMaxRunes = 1500
)

// TierName names the affinity tier shown to the reply model instead of the number.
func TierName(affinity int) string {
	switch {
	case affinity < 20:
		return "陌生"
	case affinity < 40:
		return "认识"
	case affinity < 60:
		return "熟悉"
	case affinity < 80:
		return "亲近"
	default:
		return "很要好"
	}
}

// Clamp limits affinity to [0, 100].
func Clamp(affinity int) int {
	return min(max(affinity, 0), 100)
}

// LocalDay returns t's date in loc (UTC when loc is nil) as "2006-01-02".
func LocalDay(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format(time.DateOnly)
}

// ApplyInteraction records a successful direct interaction on day: the first
// one sets affinity to InitialAffinity, the first one on each later day adds 1.
// It reports whether m changed, including when only the day did.
func ApplyInteraction(m store.MemberMemory, day string) (store.MemberMemory, bool) {
	before := m
	switch {
	case m.Affinity == nil:
		m.Affinity = newAffinity(InitialAffinity)
	case m.LastInteractionDay != day:
		m.Affinity = newAffinity(Clamp(*m.Affinity + 1))
	}
	m.LastInteractionDay = day
	m.DecaySteps = 0
	changed := before.Affinity == nil || *before.Affinity != *m.Affinity ||
		before.LastInteractionDay != m.LastInteractionDay || before.DecaySteps != m.DecaySteps
	return m, changed
}

// ApplyModelDelta applies a consolidation's affinity delta on day, limited to
// ±MaxRunDelta per run, a net of +DailyGainCap/-DailyLossCap per day and
// [0, 100] overall, and returns the amount actually applied. Members without
// an affinity are returned unchanged.
func ApplyModelDelta(m store.MemberMemory, delta int, day string) (store.MemberMemory, int) {
	if m.Affinity == nil {
		return m, 0
	}
	delta = min(max(delta, -MaxRunDelta), MaxRunDelta)
	sum := 0
	if m.ModelDeltaDay == day {
		sum = m.ModelDeltaSum
	}
	applied := min(max(sum+delta, -DailyLossCap), DailyGainCap) - sum
	next := Clamp(*m.Affinity + applied)
	applied = next - *m.Affinity
	m.Affinity = newAffinity(next)
	m.ModelDeltaDay = day
	m.ModelDeltaSum = sum + applied
	return m, applied
}

// Decay lowers affinity by 1 for every full DecayStepDays of idleness after
// the first DecayGraceDays, down to DecayFloor; affinity already below the
// floor is left alone. DecaySteps tracks the steps taken so repeating Decay
// on the same day changes nothing. It reports whether m changed.
func Decay(m store.MemberMemory, today string) (store.MemberMemory, bool) {
	if m.Affinity == nil || m.LastInteractionDay == "" {
		return m, false
	}
	last, err := time.Parse(time.DateOnly, m.LastInteractionDay)
	if err != nil {
		return m, false
	}
	now, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return m, false
	}
	idle := int(now.Sub(last).Hours() / 24)
	due := max(0, (idle-DecayGraceDays)/DecayStepDays)
	before := m
	if steps := due - m.DecaySteps; steps > 0 && *m.Affinity > DecayFloor {
		m.Affinity = newAffinity(max(*m.Affinity-steps, DecayFloor))
	}
	m.DecaySteps = due
	return m, *before.Affinity != *m.Affinity || before.DecaySteps != m.DecaySteps
}

func newAffinity(v int) *int { return &v }
