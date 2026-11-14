package gateway

import (
	"net/http"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// privateConversation resolves the {id} path value to a private conversation
// of the token's bot, reporting an error response itself on failure.
func (s *Server) privateConversation(w http.ResponseWriter, r *http.Request, tok *token) (store.ConversationRecord, bool) {
	conv, ok := s.conversation(w, r, tok)
	if !ok {
		return conv, false
	}
	if conv.Type != conloomapi.ConvPrivate {
		s.invalid(w, r, "only private conversations have a hidden state")
		return conv, false
	}
	return conv, true
}

// getAutonomy returns the private chat's status, bars, recent bar changes
// and how the bot starts the chat (core.AutonomyService.State).
func (s *Server) getAutonomy(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.privateConversation(w, r, tok)
	if !ok {
		return
	}
	state, err := s.d.Orchestrator.Autonomy().State(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// putAutonomyBar sets the bar {name} of the private chat to the body's value
// (conloomapi.AutonomyBarPut) as the token, audited as autonomy.buff.
func (s *Server) putAutonomyBar(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.privateConversation(w, r, tok)
	if !ok {
		return
	}
	var put conloomapi.AutonomyBarPut
	if !s.decodeBody(w, r, &put) {
		return
	}
	if put.Value == nil {
		s.invalid(w, r, "value is required")
		return
	}
	err := s.d.Orchestrator.Autonomy().SetBar(r.Context(), conv.Key, r.PathValue("name"), *put.Value, put.Reason,
		store.BuffSourceAPI, tok.name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

// deleteAutonomyStatus ends the private chat's status and its return timer
// as the token, audited as autonomy.status.clear.
func (s *Server) deleteAutonomyStatus(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.privateConversation(w, r, tok)
	if !ok {
		return
	}
	if err := s.d.Orchestrator.Autonomy().ClearStatus(r.Context(), conv.Key, tok.name); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}
