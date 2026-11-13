package panel

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// auditView is an audit event; Detail is the stored detail as JSON when it
// is valid JSON, otherwise as a string.
type auditView struct {
	ID            string    `json:"id"`
	BotInstanceID string    `json:"bot_instance_id"`
	Kind          string    `json:"kind"`
	Actor         string    `json:"actor"`
	Target        string    `json:"target"`
	Detail        any       `json:"detail"`
	CreatedAt     time.Time `json:"created_at"`
}

func newAuditView(a store.AuditEvent) auditView {
	v := auditView{
		ID: a.ID, BotInstanceID: a.BotInstanceID, Kind: a.Kind, Actor: a.Actor, Target: a.Target,
		Detail: a.Detail, CreatedAt: a.CreatedAt.UTC(),
	}
	if json.Valid([]byte(a.Detail)) {
		v.Detail = json.RawMessage(a.Detail)
	}
	return v
}

// listAudit pages through audit events newest first, filtered by kind and bot.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, _ *Session) {
	limit, err := pageLimit(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	q := r.URL.Query()
	events, err := s.d.Store.ListAudit(r.Context(), store.AuditFilter{
		Kind: q.Get("kind"), BotInstanceID: q.Get("bot"), Before: q.Get("before"), Limit: limit,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]auditView, 0, len(events))
	for _, e := range events {
		out = append(out, newAuditView(e))
	}
	var next *string
	if len(events) == limit {
		next = &events[len(events)-1].ID
	}
	s.writeJSON(w, http.StatusOK, struct {
		Events     []auditView `json:"events"`
		NextBefore *string     `json:"next_before"`
	}{out, next})
}
