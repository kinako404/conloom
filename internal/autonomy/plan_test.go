package autonomy

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func seeded() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func TestPlanInitiate(t *testing.T) {
	loc := shanghai(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, loc)
	r := seeded()
	base := PlanInput{Now: now, Quiet: DefaultQuiet, Loc: loc, Rand: r, SentOn: func(string) int { return 0 }}

	t.Run("after the gap, outside quiet, within a day", func(t *testing.T) {
		for _, level := range []string{conloomapi.PrivateLevelLow, conloomapi.PrivateLevelMid, conloomapi.PrivateLevelHigh} {
			for _, last := range []time.Time{now.Add(-time.Hour), now.Add(-10 * time.Hour), {}} {
				in := base
				in.Level, in.Last = level, last
				earliest := now
				if g := last.Add(MinGap(level)); g.After(now) {
					earliest = g
				}
				for range 200 {
					got, ok := PlanInitiate(in)
					if !ok {
						t.Fatalf("%s, last %v: PlanInitiate() = false, want a time", level, last)
					}
					if got.Before(earliest) {
						t.Fatalf("%s, last %v: PlanInitiate() = %v, before %v", level, last, got, earliest)
					}
					if !got.Before(earliest.Add(24*time.Hour + 15*time.Minute)) {
						t.Fatalf("%s, last %v: PlanInitiate() = %v, more than a day after %v", level, last, got, earliest)
					}
					if DefaultQuiet.Contains(got, loc) {
						t.Fatalf("%s, last %v: PlanInitiate() = %v, inside quiet hours", level, last, got)
					}
				}
			}
		}
	})

	t.Run("day at cap moves to tomorrow", func(t *testing.T) {
		in := base
		in.Level, in.Last = conloomapi.PrivateLevelMid, now.Add(-time.Hour)
		in.SentOn = func(day string) int {
			if day == "2026-11-14" {
				return DailyCap(conloomapi.PrivateLevelMid)
			}
			return 0
		}
		for range 200 {
			got, ok := PlanInitiate(in)
			if !ok {
				t.Fatal("PlanInitiate() = false, want a time tomorrow")
			}
			if day := got.In(loc).Format(time.DateOnly); day != "2026-11-15" {
				t.Fatalf("PlanInitiate() = %v, want a time on 2026-11-15", got)
			}
			if DefaultQuiet.Contains(got, loc) {
				t.Fatalf("PlanInitiate() = %v, inside quiet hours", got)
			}
		}
	})

	t.Run("every day at cap", func(t *testing.T) {
		in := base
		in.Level = conloomapi.PrivateLevelHigh
		in.SentOn = func(string) int { return DailyCap(conloomapi.PrivateLevelHigh) }
		if got, ok := PlanInitiate(in); ok {
			t.Errorf("PlanInitiate() = %v, true; want false", got)
		}
	})

	t.Run("only one hour outside quiet", func(t *testing.T) {
		in := base
		in.Level, in.Last = conloomapi.PrivateLevelMid, now.Add(-time.Hour)
		in.Quiet = Quiet{Start: 15 * 60, End: 14 * 60}
		for range 200 {
			got, ok := PlanInitiate(in)
			if !ok {
				t.Fatal("PlanInitiate() = false, want a time in 14:00–14:59")
			}
			if l := got.In(loc); l.Hour() != 14 || l.Day() != 15 {
				t.Fatalf("PlanInitiate() = %v, want 2026-11-15 14:00–14:59", l)
			}
		}
	})

	t.Run("jitter never lands in quiet or on a capped day", func(t *testing.T) {
		// Free 23:50–00:30; from 23:53 the candidates are 23:53, 00:08
		// and 00:23, and Nov 15 is at cap, so only 23:53 is left.
		in := base
		in.Level = conloomapi.PrivateLevelMid
		in.Now = time.Date(2026, 11, 14, 23, 53, 0, 0, loc)
		in.Quiet = Quiet{Start: 30, End: 23*60 + 50}
		for range 200 {
			got, ok := PlanInitiate(in)
			if !ok {
				t.Fatal("PlanInitiate() = false, want a time")
			}
			if in.Quiet.Contains(got, loc) {
				t.Fatalf("PlanInitiate() = %v, inside quiet hours", got.In(loc))
			}
		}
		in.SentOn = func(day string) int {
			if day == "2026-11-15" {
				return DailyCap(conloomapi.PrivateLevelMid)
			}
			return 0
		}
		for range 200 {
			got, ok := PlanInitiate(in)
			if !ok {
				t.Fatal("PlanInitiate() = false, want a time on Nov 14")
			}
			if l := got.In(loc); l.Day() != 14 || l.Before(in.Now) {
				t.Fatalf("PlanInitiate() = %v, want 2026-11-14 23:53–23:59", l)
			}
		}
	})

	t.Run("level off", func(t *testing.T) {
		in := base
		in.Level = conloomapi.PrivateLevelOff
		if got, ok := PlanInitiate(in); ok {
			t.Errorf("PlanInitiate(off) = %v, true; want false", got)
		}
	})
}

func TestPlanInitiate_PrefersActiveHours(t *testing.T) {
	loc := shanghai(t)
	var hist Hist
	hist[20], hist[21] = 30, 30
	hist[9], hist[22] = 1, 1
	in := PlanInput{
		Now:    time.Date(2026, 11, 14, 15, 0, 0, 0, loc),
		Last:   time.Date(2026, 11, 14, 10, 0, 0, 0, loc),
		Level:  conloomapi.PrivateLevelMid,
		Quiet:  DefaultQuiet,
		Hist:   hist,
		Enough: true,
		SentOn: func(string) int { return 0 },
		Loc:    loc,
		Rand:   seeded(),
	}
	active := func() float64 {
		n := 0
		for range 1000 {
			got, ok := PlanInitiate(in)
			if !ok {
				t.Fatal("PlanInitiate() = false, want a time")
			}
			if h := got.In(loc).Hour(); h == 20 || h == 21 {
				n++
			}
		}
		return float64(n) / 1000
	}
	if share := active(); share < 0.7 {
		t.Errorf("with enough data %.1f%% land in 20:00–21:59, want at least 70%%", share*100)
	}
	in.Enough = false
	if share := active(); share > 0.3 {
		t.Errorf("without enough data %.1f%% land in 20:00–21:59, want the hours weighted equally", share*100)
	}
}

func TestPlanFollowUp(t *testing.T) {
	loc := shanghai(t)
	r := seeded()

	now := time.Date(2026, 11, 14, 12, 0, 0, 0, loc)
	for range 500 {
		got := PlanFollowUp(now, DefaultQuiet, loc, r)
		if d := got.Sub(now); d < 90*time.Minute || d > 4*time.Hour {
			t.Fatalf("PlanFollowUp(12:00) = %v, %v later; want 1.5–4h", got, d)
		}
	}

	tests := []struct {
		name string
		now  time.Time
		q    Quiet
	}{
		{"quiet starts before midnight", time.Date(2026, 11, 14, 20, 0, 0, 0, loc), Quiet{Start: 21 * 60, End: 8 * 60}},
		{"quiet starts after midnight", time.Date(2026, 11, 14, 23, 0, 0, 0, loc), DefaultQuiet},
	}
	for _, tt := range tests {
		from := time.Date(2026, 11, 15, 8, 0, 0, 0, loc)
		for range 500 {
			got := PlanFollowUp(tt.now, tt.q, loc, r)
			if got.Before(from) || !got.Before(from.Add(time.Hour)) {
				t.Fatalf("%s: PlanFollowUp(%v) = %v, want 2026-11-15 08:00–08:59", tt.name, tt.now, got.In(loc))
			}
		}
	}

	// Only 08:00–08:29 is outside quiet: the minutes after it stay in it.
	short := Quiet{Start: 8*60 + 30, End: 8 * 60}
	from := time.Date(2026, 11, 15, 8, 0, 0, 0, loc)
	for range 500 {
		got := PlanFollowUp(now, short, loc, r)
		if got.Before(from) || !got.Before(from.Add(30*time.Minute)) {
			t.Fatalf("short gap: PlanFollowUp(%v) = %v, want 2026-11-15 08:00–08:29", now, got.In(loc))
		}
	}
}
