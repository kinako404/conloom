package usage

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

const dayLayout = "2006-01-02"

// Row sums the calls of one group key. The latencies are nearest-rank
// percentiles of the successful calls' latencies, nil when there are none or
// percentiles were not asked for.
type Row struct {
	Key              string `json:"key"`
	Calls            int64  `json:"calls"`
	Errors           int64  `json:"errors"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	LatencyP50MS     *int64 `json:"latency_p50_ms"`
	LatencyP95MS     *int64 `json:"latency_p95_ms"`
}

// Day holds the rows of one local day, sorted by Key.
type Day struct {
	Day  string `json:"day"` // YYYY-MM-DD in the report's time zone
	Rows []Row  `json:"rows"`
}

// Report is a usage summary over the local days From..To inclusive.
type Report struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Group    string `json:"group"`
	Timezone string `json:"timezone"`
	Days     []Day  `json:"days"`   // every day From..To inclusive, Rows sorted by Key, empty slice not null
	Totals   []Row  `json:"totals"` // sorted by total tokens desc, then Key
}

// groupKeys are the dimensions a report can group by.
var groupKeys = map[string]func(bot, purpose, role, model string) string{
	"model":   func(_, _, _, model string) string { return model },
	"role":    func(_, _, role, _ string) string { return role },
	"purpose": func(_, purpose, _, _ string) string { return purpose },
	"bot":     func(bot, _, _, _ string) string { return bot },
}

// Range returns the instants bounding the local days from..to (YYYY-MM-DD)
// in loc: [local midnight of from, local midnight after to). It is an error
// for a date not to parse or from to come after to.
func Range(loc *time.Location, from, to string) (start, end time.Time, err error) {
	start, err = time.ParseInLocation(dayLayout, from, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("usage: bad from date %q", from)
	}
	last, err := time.ParseInLocation(dayLayout, to, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("usage: bad to date %q", to)
	}
	if last.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("usage: from %s is after to %s", from, to)
	}
	return start, last.AddDate(0, 0, 1), nil
}

// Summarize sums buckets per local day in loc and group key ("model",
// "role", "purpose" or "bot") over the days from..to inclusive, plus totals
// over the whole range. A bucket belongs to the local day of its Start and a
// sample to that of its At; those outside the range are ignored. With
// percentiles, rows get p50 and p95 of their samples' latencies. It is an
// error for the group to be unknown or the dates to be invalid (see Range).
func Summarize(buckets []store.UsageBucket, samples []store.LatencySample, loc *time.Location,
	from, to string, group string, percentiles bool) (Report, error) {
	keyOf, ok := groupKeys[group]
	if !ok {
		return Report{}, fmt.Errorf("usage: unknown group %q", group)
	}
	start, end, err := Range(loc, from, to)
	if err != nil {
		return Report{}, err
	}
	rep := Report{From: from, To: to, Group: group, Timezone: loc.String(), Days: []Day{}}
	index := map[string]int{}
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		day := d.Format(dayLayout)
		index[day] = len(rep.Days)
		rep.Days = append(rep.Days, Day{Day: day})
	}
	dayOf := func(t time.Time) (int, bool) {
		if t.Before(start) || !t.Before(end) {
			return 0, false
		}
		i, ok := index[t.In(loc).Format(dayLayout)]
		return i, ok
	}

	daily := make([]map[string]*Row, len(rep.Days))
	for i := range daily {
		daily[i] = map[string]*Row{}
	}
	totals := map[string]*Row{}
	row := func(rows map[string]*Row, key string) *Row {
		r, ok := rows[key]
		if !ok {
			r = &Row{Key: key}
			rows[key] = r
		}
		return r
	}
	for _, b := range buckets {
		i, ok := dayOf(b.Start)
		if !ok {
			continue
		}
		key := keyOf(b.BotInstanceID, b.Purpose, b.Role, b.Model)
		for _, r := range []*Row{row(daily[i], key), row(totals, key)} {
			r.Calls += b.Calls
			r.Errors += b.Errors
			r.PromptTokens += b.PromptTokens
			r.CompletionTokens += b.CompletionTokens
			r.ReasoningTokens += b.ReasoningTokens
		}
	}

	if percentiles {
		dailyMS := make([]map[string][]int64, len(rep.Days))
		for i := range dailyMS {
			dailyMS[i] = map[string][]int64{}
		}
		totalMS := map[string][]int64{}
		for _, s := range samples {
			i, ok := dayOf(s.At)
			if !ok {
				continue
			}
			key := keyOf(s.BotInstanceID, s.Purpose, s.Role, s.Model)
			dailyMS[i][key] = append(dailyMS[i][key], s.LatencyMS)
			totalMS[key] = append(totalMS[key], s.LatencyMS)
		}
		for i := range daily {
			setPercentiles(daily[i], dailyMS[i])
		}
		setPercentiles(totals, totalMS)
	}

	for i, rows := range daily {
		rep.Days[i].Rows = sortedRows(rows, func(a, b Row) int { return cmp.Compare(a.Key, b.Key) })
	}
	rep.Totals = sortedRows(totals, func(a, b Row) int {
		return cmp.Or(cmp.Compare(tokens(b), tokens(a)), cmp.Compare(a.Key, b.Key))
	})
	return rep, nil
}

// tokens is a row's total tokens. Reasoning tokens are part of the
// completion tokens, so they are not added again.
func tokens(r Row) int64 { return r.PromptTokens + r.CompletionTokens }

// setPercentiles sets the latency percentiles of the rows that have samples.
func setPercentiles(rows map[string]*Row, ms map[string][]int64) {
	for key, r := range rows {
		vals := ms[key]
		if len(vals) == 0 {
			continue
		}
		slices.Sort(vals)
		p50, p95 := nearestRank(vals, 50), nearestRank(vals, 95)
		r.LatencyP50MS, r.LatencyP95MS = &p50, &p95
	}
}

// nearestRank returns the p-th percentile of the sorted, non-empty vals: the
// value at rank ceil(p/100 * n).
func nearestRank(vals []int64, p float64) int64 {
	rank := int(math.Ceil(p / 100 * float64(len(vals))))
	return vals[min(max(rank, 1), len(vals))-1]
}

func sortedRows(rows map[string]*Row, compare func(a, b Row) int) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, compare)
	return out
}
