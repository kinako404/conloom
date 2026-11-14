package panel

import (
	"context"
	"net/http"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// AutonomyService shows and changes the hidden state of private chats, their
// status and bars; *core.AutonomyService is one.
type AutonomyService interface {
	State(ctx context.Context, convKey string) (conloomapi.AutonomyState, error)
	SetBar(ctx context.Context, convKey, name string, value int, reason, source, actor string) error
	ClearStatus(ctx context.Context, convKey, actor string) error
}

// autonomyService is Deps.Autonomy, writing 503 when there is none.
func (s *Server) autonomyService(w http.ResponseWriter, r *http.Request) (AutonomyService, bool) {
	if s.d.Autonomy == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "the hidden state is not available")
		return nil, false
	}
	return s.d.Autonomy, true
}

// getAutonomy shows the private chat's status, bars, recent bar changes and
// how the bot starts the chat. Activities and reasons are data, which the
// page shows as plain text.
func (s *Server) getAutonomy(w http.ResponseWriter, r *http.Request, _ *Session) {
	svc, ok := s.autonomyService(w, r)
	if !ok {
		return
	}
	conv, ok := s.privateConversation(w, r)
	if !ok {
		return
	}
	state, err := svc.State(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, state)
}

// putAutonomyBar sets the bar {name} of the private chat to the body's value
// (conloomapi.AutonomyBarPut), audited as autonomy.buff by the service.
func (s *Server) putAutonomyBar(w http.ResponseWriter, r *http.Request, sess *Session) {
	svc, ok := s.autonomyService(w, r)
	if !ok {
		return
	}
	conv, ok := s.privateConversation(w, r)
	if !ok {
		return
	}
	var put conloomapi.AutonomyBarPut
	if !s.decode(w, r, &put) {
		return
	}
	if put.Value == nil {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "value is required")
		return
	}
	if err := svc.SetBar(r.Context(), conv.Key, r.PathValue("name"), *put.Value, put.Reason, store.BuffSourcePanel, panelActor(sess)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAutonomyStatus ends the private chat's status and its return timer,
// audited as autonomy.status.clear by the service.
func (s *Server) deleteAutonomyStatus(w http.ResponseWriter, r *http.Request, sess *Session) {
	svc, ok := s.autonomyService(w, r)
	if !ok {
		return
	}
	conv, ok := s.privateConversation(w, r)
	if !ok {
		return
	}
	if err := svc.ClearStatus(r.Context(), conv.Key, panelActor(sess)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
