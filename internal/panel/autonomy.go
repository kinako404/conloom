package panel

import (
	"context"
	"net/http"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// AutonomyService shows and changes the hidden state of private chats, their
// status, bars, effects and scene; *core.AutonomyService is one.
type AutonomyService interface {
	State(ctx context.Context, convKey string) (conloomapi.AutonomyState, error)
	SetBar(ctx context.Context, convKey, name string, value int, reason, source, actor string) error
	RemoveBar(ctx context.Context, convKey, name, source, actor string) error
	RemoveEffect(ctx context.Context, convKey, name, source, actor string) error
	ClearStatus(ctx context.Context, convKey, actor string) error
	Scene(ctx context.Context, convKey string) (conloomapi.AutonomyScene, error)
	SetScene(ctx context.Context, convKey, mode, place, source, actor string) error
}

// autonomyService is Deps.Autonomy, writing 503 when there is none.
func (s *Server) autonomyService(w http.ResponseWriter, r *http.Request) (AutonomyService, bool) {
	if s.d.Autonomy == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "the hidden state is not available")
		return nil, false
	}
	return s.d.Autonomy, true
}

// getAutonomy shows the private chat's status, bars, effects, their recent
// changes, how the bot starts the chat and its scene. Activities, reasons,
// places and the bot's names and descriptions are data, which the page shows
// as plain text.
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

// putAutonomyScene sets the private chat's scene to the body's mode and place
// (conloomapi.AutonomyScenePut), audited as autonomy.scene.set by the
// service; an unknown mode or a place that is too long is 400.
func (s *Server) putAutonomyScene(w http.ResponseWriter, r *http.Request, sess *Session) {
	svc, ok := s.autonomyService(w, r)
	if !ok {
		return
	}
	conv, ok := s.privateConversation(w, r)
	if !ok {
		return
	}
	var put conloomapi.AutonomyScenePut
	if !s.decode(w, r, &put) {
		return
	}
	if err := svc.SetScene(r.Context(), conv.Key, put.Mode, put.Place, store.BuffSourcePanel, panelActor(sess)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAutonomyBar removes the bar {name} the bot added from the private
// chat, audited as autonomy.bar.remove by the service; a bar of the persona
// is 400.
func (s *Server) deleteAutonomyBar(w http.ResponseWriter, r *http.Request, sess *Session) {
	svc, ok := s.autonomyService(w, r)
	if !ok {
		return
	}
	conv, ok := s.privateConversation(w, r)
	if !ok {
		return
	}
	if err := svc.RemoveBar(r.Context(), conv.Key, r.PathValue("name"), store.BuffSourcePanel, panelActor(sess)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAutonomyEffect ends the private chat's active effect {name} early,
// audited as autonomy.effect.remove by the service.
func (s *Server) deleteAutonomyEffect(w http.ResponseWriter, r *http.Request, sess *Session) {
	svc, ok := s.autonomyService(w, r)
	if !ok {
		return
	}
	conv, ok := s.privateConversation(w, r)
	if !ok {
		return
	}
	if err := svc.RemoveEffect(r.Context(), conv.Key, r.PathValue("name"), store.BuffSourcePanel, panelActor(sess)); err != nil {
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
