package panel

import (
	"net/http"
	"slices"
	"time"

	dto "github.com/prometheus/client_model/go"
)

type overviewBot struct {
	ID         string     `json:"id"`
	Platform   string     `json:"platform"`
	Name       string     `json:"name"`
	LastSeenAt *time.Time `json:"last_seen_at"` // last call to the core since this process started
}

// overviewLive is the process's current state. The gauges are null when the
// registry does not have them.
type overviewLive struct {
	QueueDepth           *int64   `json:"queue_depth"`
	OutboxPending        int      `json:"outbox_pending"`
	EmbeddingBacklog     *int64   `json:"embedding_backlog"`
	FTSBackfillRemaining *int64   `json:"fts_backfill_remaining"`
	BreakersOpen         []string `json:"breakers_open"` // roles, sorted
}

type overviewToday struct {
	Calls            int64 `json:"calls"`
	Errors           int64 `json:"errors"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

type overviewResponse struct {
	Version   string        `json:"version"`
	Commit    string        `json:"commit"`
	StartedAt time.Time     `json:"started_at"`
	Timezone  string        `json:"timezone"`
	Bots      []overviewBot `json:"bots"`
	Live      overviewLive  `json:"live"`
	Today     overviewToday `json:"today"` // the model calls of the current day in the server time zone
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request, _ *Session) {
	ctx := r.Context()
	loc := s.location()
	y, m, d := s.now().In(loc).Date()
	buckets, err := s.d.Store.UsageBuckets(ctx, time.Date(y, m, d, 0, 0, 0, 0, loc), time.Date(y, m, d+1, 0, 0, 0, 0, loc))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pending, err := s.d.Store.CountPendingOutbox(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	mfs := s.gather(r)
	resp := overviewResponse{
		Version: s.d.Version, Commit: s.d.Commit, StartedAt: s.d.StartedAt.UTC(), Timezone: loc.String(),
		Bots: s.overviewBots(),
		Live: overviewLive{
			QueueDepth:           gaugeCount(mfs, "conloom_queue_depth"),
			OutboxPending:        pending,
			EmbeddingBacklog:     gaugeCount(mfs, "conloom_embedding_backlog"),
			FTSBackfillRemaining: gaugeCount(mfs, "conloom_fts_backfill_remaining"),
			BreakersOpen:         openBreakers(mfs),
		},
	}
	t := &resp.Today
	for _, b := range buckets {
		t.Calls += b.Calls
		t.Errors += b.Errors
		t.PromptTokens += b.PromptTokens
		t.CompletionTokens += b.CompletionTokens
		t.ReasoningTokens += b.ReasoningTokens
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// overviewBots lists the configured bots with when each last called the core.
func (s *Server) overviewBots() []overviewBot {
	seen := s.d.Presence.Snapshot()
	bots := make([]overviewBot, 0, len(s.d.Config.Bots))
	for _, b := range s.d.Config.Bots {
		bot := overviewBot{ID: b.ID, Platform: b.Platform, Name: b.Name}
		if at, ok := seen[b.ID]; ok {
			at = at.UTC()
			bot.LastSeenAt = &at
		}
		bots = append(bots, bot)
	}
	return bots
}

// openBreakers returns the roles whose conloom_breaker_open gauge is set,
// sorted.
func openBreakers(mfs []*dto.MetricFamily) []string {
	roles := []string{}
	for _, m := range family(mfs, "conloom_breaker_open").GetMetric() {
		if m.GetGauge().GetValue() == 0 {
			continue
		}
		for _, l := range m.GetLabel() {
			if l.GetName() == "role" {
				roles = append(roles, l.GetValue())
			}
		}
	}
	slices.Sort(roles)
	return roles
}
