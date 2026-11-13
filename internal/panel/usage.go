package panel

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// defaultUsageDays is the range of a usage report without from or to,
	// counting the last day.
	defaultUsageDays = 7
	// maxUsageDays bounds the days of a usage report.
	maxUsageDays = 400
	// maxPercentileDays bounds the days of a report with latency percentiles.
	maxPercentileDays = 90
)

// usageGroups are the group values of a usage report.
var usageGroups = map[string]bool{"model": true, "role": true, "purpose": true, "bot": true}

// usageReport serves the usage of the local days from..to (YYYY-MM-DD in the
// server time zone) grouped by group. to defaults to today, from to the 7th
// day up to to, and group to model.
func (s *Server) usageReport(w http.ResponseWriter, r *http.Request, _ *Session) {
	q := r.URL.Query()
	loc := s.location()
	from, to, group := q.Get("from"), q.Get("to"), q.Get("group")
	if group == "" {
		group = "model"
	}
	if !usageGroups[group] {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "group must be model, role, purpose or bot")
		return
	}
	if to == "" {
		to = s.now().In(loc).Format(time.DateOnly)
	}
	if from == "" {
		last, err := time.Parse(time.DateOnly, to)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "to must be a date (YYYY-MM-DD)")
			return
		}
		from = last.AddDate(0, 0, 1-defaultUsageDays).Format(time.DateOnly)
	}
	start, end, err := usage.Range(loc, from, to)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	// Rounding absorbs the hour a daylight saving change adds or removes.
	days := int(end.Sub(start).Round(24*time.Hour) / (24 * time.Hour))
	if days > maxUsageDays {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			"the range must be at most "+strconv.Itoa(maxUsageDays)+" days")
		return
	}

	buckets, err := s.d.Store.UsageBuckets(r.Context(), start, end)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	percentiles := days <= maxPercentileDays
	var samples []store.LatencySample
	if percentiles {
		if samples, err = s.d.Store.LatencySamples(r.Context(), start, end); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	rep, err := usage.Summarize(buckets, samples, loc, from, to, group, percentiles)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, rep)
}

type usageCallsResponse struct {
	Calls      []modelCallView `json:"calls"`
	NextBefore *int64          `json:"next_before"` // the before of the next page, null on the last
}

// usageCalls serves model calls, newest first, a page at a time. from and to
// are local days in the server time zone, each optional; model, purpose and
// conversation filter exactly.
func (s *Server) usageCalls(w http.ResponseWriter, r *http.Request, _ *Session) {
	q := r.URL.Query()
	limit, err := pageLimit(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	f := store.ModelCallFilter{
		Model: q.Get("model"), Purpose: q.Get("purpose"), ConversationKey: q.Get("conversation"), Limit: limit + 1,
	}
	loc := s.location()
	if v := q.Get("from"); v != "" {
		if f.From, _, err = usage.Range(loc, v, v); err != nil {
			s.badRequest(w, r, err)
			return
		}
	}
	if v := q.Get("to"); v != "" {
		if _, f.To, err = usage.Range(loc, v, v); err != nil {
			s.badRequest(w, r, err)
			return
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To) {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "from must not be after to")
		return
	}
	if v := q.Get("before"); v != "" {
		if f.BeforeID, err = strconv.ParseInt(v, 10, 64); err != nil || f.BeforeID < 1 {
			s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "before must be a positive integer")
			return
		}
	}

	calls, err := s.d.Store.ListModelCalls(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := usageCallsResponse{Calls: make([]modelCallView, 0, min(len(calls), limit))}
	if len(calls) > limit {
		calls = calls[:limit]
		next := calls[limit-1].ID
		resp.NextBefore = &next
	}
	for _, c := range calls {
		resp.Calls = append(resp.Calls, newModelCallView(c))
	}
	s.writeJSON(w, http.StatusOK, resp)
}
