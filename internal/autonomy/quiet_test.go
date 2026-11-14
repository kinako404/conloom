package autonomy

import (
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func shanghai(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// atHours returns one message at each of hours (local, minute 0) on each of
// days consecutive local days starting 2026-11-01.
func atHours(loc *time.Location, days int, hours ...int) []time.Time {
	var times []time.Time
	for d := range days {
		for _, h := range hours {
			times = append(times, time.Date(2026, 11, 1+d, h, 0, 0, 0, loc))
		}
	}
	return times
}

func TestQuiet_ContainsWraps(t *testing.T) {
	loc := shanghai(t)
	at := func(h, m int) time.Time { return time.Date(2026, 11, 14, h, m, 0, 0, loc) }
	wrap := Quiet{Start: 23 * 60, End: 7 * 60}
	tests := []struct {
		name string
		q    Quiet
		t    time.Time
		want bool
	}{
		{"wrap 02:00", wrap, at(2, 0), true},
		{"wrap 23:00", wrap, at(23, 0), true},
		{"wrap 06:59", wrap, at(6, 59), true},
		{"wrap 07:00", wrap, at(7, 0), false},
		{"wrap 12:00", wrap, at(12, 0), false},
		{"wrap 22:59", wrap, at(22, 59), false},
		{"default 00:30", DefaultQuiet, at(0, 30), true},
		{"default 00:29", DefaultQuiet, at(0, 29), false},
		{"default 07:59:59", DefaultQuiet, time.Date(2026, 11, 14, 7, 59, 59, 0, loc), true},
		{"default 08:00", DefaultQuiet, at(8, 0), false},
		{"equal start and end is empty", Quiet{Start: 480, End: 480}, at(8, 0), false},
	}
	for _, tt := range tests {
		if got := tt.q.Contains(tt.t, loc); got != tt.want {
			t.Errorf("%s: %v.Contains(%v) = %v, want %v", tt.name, tt.q, tt.t, got, tt.want)
		}
	}
	// 02:00 in Shanghai is 18:00 UTC the day before: the location decides.
	if DefaultQuiet.Contains(at(2, 0), time.UTC) {
		t.Error("DefaultQuiet.Contains(18:00 UTC, UTC) = true, want false")
	}
}

func TestQuiet_String(t *testing.T) {
	for q, want := range map[Quiet]string{
		DefaultQuiet:                                  "00:30–08:00",
		{Start: 23 * 60, End: 7 * 60}:                 "23:00–07:00",
		{Start: 0, End: 23*60 + 59}:                   "00:00–23:59",
		{Start: 22 * 60, End: 6 * 60, Inferred: true}: "22:00–06:00",
	} {
		if got := q.String(); got != want {
			t.Errorf("%#v.String() = %q, want %q", q, got, want)
		}
	}
}

func TestQuiet_EndAfter(t *testing.T) {
	loc := shanghai(t)
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, loc) }
	wrap := Quiet{Start: 23 * 60, End: 7 * 60}
	tests := []struct {
		name string
		q    Quiet
		t    time.Time
		want time.Time
	}{
		{"default inside", DefaultQuiet, at(14, 2, 0), at(14, 8, 0)},
		{"default at its end", DefaultQuiet, at(14, 8, 0), at(15, 8, 0)},
		{"default before", DefaultQuiet, at(14, 0, 10), at(14, 8, 0)},
		{"wrap before midnight", wrap, at(14, 23, 30), at(15, 7, 0)},
		{"wrap after midnight", wrap, at(15, 1, 0), at(15, 7, 0)},
		{"utc input", DefaultQuiet, time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC), at(14, 8, 0)},
	}
	for _, tt := range tests {
		if got := tt.q.EndAfter(tt.t, loc); !got.Equal(tt.want) {
			t.Errorf("%s: EndAfter(%v) = %v, want %v", tt.name, tt.t, got.In(loc), tt.want)
		}
	}
}

func TestHistOf(t *testing.T) {
	loc := shanghai(t)
	times := []time.Time{
		time.Date(2026, 11, 14, 9, 5, 0, 0, loc),
		time.Date(2026, 11, 14, 9, 55, 0, 0, loc),
		time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC), // 23:00 in Shanghai
	}
	var want Hist
	want[9], want[23] = 2, 1
	if got := HistOf(times, loc); got != want {
		t.Errorf("HistOf() = %v, want %v", got, want)
	}
}

func TestEnoughData(t *testing.T) {
	loc := shanghai(t)
	if EnoughData(atHours(loc, 13, 10, 12, 14)[:39], loc) {
		t.Error("39 messages over 13 days: EnoughData = true, want false")
	}
	if EnoughData(atHours(loc, 4, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19), loc) {
		t.Error("40 messages over 4 days: EnoughData = true, want false")
	}
	if !EnoughData(atHours(loc, 5, 10, 11, 12, 13, 14, 15, 16, 17), loc) {
		t.Error("40 messages over 5 days: EnoughData = false, want true")
	}

	// Five UTC days, but 17:00 UTC on Nov 1 and 10:00 UTC on Nov 2 are both
	// Nov 2 in Shanghai: four local days there.
	var times []time.Time
	for i := range 8 {
		times = append(times,
			time.Date(2026, 11, 1, 17, i, 0, 0, time.UTC),
			time.Date(2026, 11, 2, 10, i, 0, 0, time.UTC),
			time.Date(2026, 11, 3, 10, i, 0, 0, time.UTC),
			time.Date(2026, 11, 4, 10, i, 0, 0, time.UTC),
			time.Date(2026, 11, 5, 10, i, 0, 0, time.UTC))
	}
	if !EnoughData(times, time.UTC) {
		t.Error("40 messages over 5 UTC days: EnoughData(UTC) = false, want true")
	}
	if EnoughData(times, loc) {
		t.Error("40 messages over 4 Shanghai days: EnoughData(Shanghai) = true, want false")
	}
}

func TestInferQuiet(t *testing.T) {
	loc := shanghai(t)
	tests := []struct {
		name  string
		times []time.Time
		want  Quiet
	}{
		{"39 messages", atHours(loc, 13, 10, 12, 14)[:39], DefaultQuiet},
		{"4 days", atHours(loc, 4, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19), DefaultQuiet},
		{"none", nil, DefaultQuiet},
		{
			"active 10:00-23:59 only",
			atHours(loc, 5, 10, 12, 14, 16, 18, 20, 22, 23),
			Quiet{Start: 0, End: 8 * 60, Inferred: true},
		},
		{
			"every hour alike ties at 00:00",
			atHours(loc, 5, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23),
			Quiet{Start: 0, End: 8 * 60, Inferred: true},
		},
		{
			// Two empty 8-hour windows, 09:00–17:00 and 22:00–06:00: 22:00
			// is two hours from 00:00 around the clock, 09:00 is nine.
			"tie picks start closest to 00:00",
			atHours(loc, 5, 6, 7, 8, 17, 18, 19, 20, 21),
			Quiet{Start: 22 * 60, End: 6 * 60, Inferred: true},
		},
		{
			"fewest messages wins over closeness",
			append(atHours(loc, 5, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23),
				atHours(loc, 5, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 21, 22, 23)...),
			Quiet{Start: 13 * 60, End: 21 * 60, Inferred: true},
		},
	}
	for _, tt := range tests {
		got := InferQuiet(tt.times, loc)
		if got != tt.want {
			t.Errorf("%s: InferQuiet() = %#v, want %#v", tt.name, got, tt.want)
		}
		if got.Start == got.End {
			t.Errorf("%s: InferQuiet() = %#v, start equals end", tt.name, got)
		}
	}
}

func TestResolveQuiet_PolicyWins(t *testing.T) {
	loc := shanghai(t)
	enough := atHours(loc, 5, 10, 12, 14, 16, 18, 20, 22, 23)
	set := conloomapi.PrivateProactivePolicy{QuietStart: "23:00", QuietEnd: "07:00"}
	if got, want := ResolveQuiet(set, enough, loc), (Quiet{Start: 23 * 60, End: 7 * 60}); got != want {
		t.Errorf("ResolveQuiet(policy 23:00–07:00, enough data) = %#v, want %#v", got, want)
	}
	if got, want := ResolveQuiet(set, nil, loc), (Quiet{Start: 23 * 60, End: 7 * 60}); got != want {
		t.Errorf("ResolveQuiet(policy 23:00–07:00, no data) = %#v, want %#v", got, want)
	}
	if got, want := ResolveQuiet(conloomapi.PrivateProactivePolicy{}, enough, loc), (Quiet{Start: 0, End: 8 * 60, Inferred: true}); got != want {
		t.Errorf("ResolveQuiet(empty policy, enough data) = %#v, want %#v", got, want)
	}
	if got := ResolveQuiet(conloomapi.PrivateProactivePolicy{Level: conloomapi.PrivateLevelHigh}, nil, loc); got != DefaultQuiet {
		t.Errorf("ResolveQuiet(empty quiet, no data) = %#v, want DefaultQuiet", got)
	}
	equal := conloomapi.PrivateProactivePolicy{QuietStart: "08:00", QuietEnd: "08:00"}
	if got := ResolveQuiet(equal, nil, loc); got != DefaultQuiet {
		t.Errorf("ResolveQuiet(equal start and end) = %#v, want DefaultQuiet", got)
	}
}
