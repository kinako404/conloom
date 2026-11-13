package gateway

import (
	"errors"
	"net/http"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// memoryConversation resolves the {id} path value to a group or private
// conversation of the token's bot, reporting an error response itself on
// failure.
func (s *Server) memoryConversation(w http.ResponseWriter, r *http.Request, tok *token) (store.ConversationRecord, bool) {
	conv, ok := s.conversation(w, r, tok)
	if !ok {
		return conv, false
	}
	if conv.Type != conloomapi.ConvGroup && conv.Type != conloomapi.ConvPrivate {
		s.invalid(w, r, "memory is only kept for group and private conversations")
		return conv, false
	}
	return conv, true
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.memoryConversation(w, r, tok)
	if !ok {
		return
	}
	state, err := s.d.MemoryAdmin.View(r.Context(), conv)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// putMemberMemory sets a member's note, affinity or both (memory.Admin.EditMember).
func (s *Server) putMemberMemory(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.memoryConversation(w, r, tok)
	if !ok {
		return
	}
	var put conloomapi.MemberMemoryPut
	if !s.decodeBody(w, r, &put) {
		return
	}
	m, err := s.d.MemoryAdmin.EditMember(r.Context(), conv, r.PathValue("user_id"), put, store.RevisionAPI, tok.name)
	if !s.memoryChanged(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// deleteMemberMemory forgets a member as /忘记我 does (memory.Admin.ForgetMember).
func (s *Server) deleteMemberMemory(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.memoryConversation(w, r, tok)
	if !ok {
		return
	}
	err := s.d.MemoryAdmin.ForgetMember(r.Context(), conv, r.PathValue("user_id"), tok.name)
	if !s.memoryChanged(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

// memoryChanged reports whether a memory change was stored. A failed audit of
// a stored change is only logged.
func (s *Server) memoryChanged(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, memory.ErrAudit) {
		s.log.Warn("memory changed but audit failed", "trace_id", traceID(r.Context()), "path", r.URL.Path, "error", err)
		return true
	}
	s.fail(w, r, err)
	return false
}

// consolidateMemory asks for a consolidation of the conversation; it is
// refused with 409 MEMORY_DISABLED while memory is off and 409 PAUSED while
// the conversation is paused.
func (s *Server) consolidateMemory(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.memoryConversation(w, r, tok)
	if !ok {
		return
	}
	pv, err := s.d.Policies.Current(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p := pv.Policy; !p.Memory.Enabled || !p.Concurrency.AllowLongRequests {
		s.writeError(w, r, http.StatusConflict, conloomapi.CodeMemoryDisabled, "memory is not enabled for this conversation", false, nil)
		return
	}
	if pv.Policy.Concurrency.MaxInteractiveRequests == 0 {
		s.writeError(w, r, http.StatusConflict, conloomapi.CodePaused, "the conversation is paused; memory is not consolidated", false, nil)
		return
	}
	if s.d.Memory != nil {
		s.d.Memory.Trigger(conv.Key)
	}
	writeJSON(w, http.StatusAccepted, struct {
		Accepted bool `json:"accepted"`
	}{true})
}
