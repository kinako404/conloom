package panel

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func (s *Server) writePolicy(w http.ResponseWriter, pv store.PolicyVersion) {
	s.writeJSON(w, http.StatusOK, struct {
		Current policyVersionView `json:"current"`
	}{newPolicyVersionView(pv)})
}

func (s *Server) writePersona(w http.ResponseWriter, pv store.PersonaVersion) {
	s.writeJSON(w, http.StatusOK, struct {
		Current personaVersionView `json:"current"`
	}{newPersonaVersionView(pv)})
}

// versionStored reports whether a policy or persona change succeeded. An
// error returned together with a stored version (a failed audit write) is
// only logged.
func (s *Server) versionStored(w http.ResponseWriter, r *http.Request, version int, err error) bool {
	if err == nil {
		return true
	}
	if version > 0 {
		s.log.Warn("version stored but follow-up failed", "trace_id", traceID(r.Context()), "path", r.URL.Path, "error", err)
		return true
	}
	s.fail(w, r, err)
	return false
}

// memoryStored reports whether a member memory change was stored. A failed
// audit of a stored change is only logged.
func (s *Server) memoryStored(w http.ResponseWriter, r *http.Request, err error) bool {
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

func (s *Server) putPolicy(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	var put conloomapi.PolicyPut
	if !s.decode(w, r, &put) {
		return
	}
	pv, err := s.d.Policies.Update(r.Context(), conv.Key, put, panelActor(sess))
	if !s.versionStored(w, r, pv.Version, err) {
		return
	}
	s.writePolicy(w, pv)
}

// restorePolicy stores a stored policy version again as the newest version.
func (s *Server) restorePolicy(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	var req struct {
		Version int    `json:"version"`
		Reason  string `json:"reason"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.Version < 1 {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "version must be a positive integer")
		return
	}
	versions, err := s.d.Store.ListPolicyVersions(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var old *store.PolicyVersion
	for i := range versions {
		if versions[i].Version == req.Version {
			old = &versions[i]
			break
		}
	}
	if old == nil {
		s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, fmt.Sprintf("policy version %d not found", req.Version))
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("restore v%d", req.Version)
	}
	pv, err := s.d.Policies.Update(r.Context(), conv.Key,
		conloomapi.PolicyPut{Policy: old.Policy, Reason: reason, ProactivePresent: true, MemoryPresent: true}, panelActor(sess))
	if !s.versionStored(w, r, pv.Version, err) {
		return
	}
	s.writePolicy(w, pv)
}

func (s *Server) pausePolicy(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	pv, err := s.d.Policies.Pause(r.Context(), conv.Key, panelActor(sess))
	if !s.versionStored(w, r, pv.Version, err) {
		return
	}
	s.writePolicy(w, pv)
}

func (s *Server) resumePolicy(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	pv, err := s.d.Policies.Resume(r.Context(), conv.Key, panelActor(sess))
	if !s.versionStored(w, r, pv.Version, err) {
		return
	}
	s.writePolicy(w, pv)
}

// putPersona replaces, rolls back or applies a template to the persona; a
// replaced persona is recorded with the bot's configured template.
func (s *Server) putPersona(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	bot, ok := s.d.Config.Bot(conv.BotInstanceID)
	if !ok {
		s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, "bot not configured")
		return
	}
	var put conloomapi.PersonaPut
	if !s.decode(w, r, &put) {
		return
	}
	pv, err := s.d.Personas.Update(r.Context(), conv.Key, bot.PersonaTemplate, put, panelActor(sess))
	if !s.versionStored(w, r, pv.Version, err) {
		return
	}
	s.writePersona(w, pv)
}

// putMemberMemory sets a member's note, affinity or both (memory.Admin.EditMember).
func (s *Server) putMemberMemory(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.memoryConversation(w, r)
	if !ok {
		return
	}
	var put conloomapi.MemberMemoryPut
	if !s.decode(w, r, &put) {
		return
	}
	m, err := s.d.MemoryAdmin.EditMember(r.Context(), conv, r.PathValue("user_id"), put, store.RevisionPanel, panelActor(sess))
	if !s.memoryStored(w, r, err) {
		return
	}
	s.writeJSON(w, http.StatusOK, m)
}

// deleteMemberMemory forgets a member as /忘记我 does (memory.Admin.ForgetMember).
func (s *Server) deleteMemberMemory(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.memoryConversation(w, r)
	if !ok {
		return
	}
	err := s.d.MemoryAdmin.ForgetMember(r.Context(), conv, r.PathValue("user_id"), store.RevisionPanel, panelActor(sess))
	if !s.memoryStored(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// consolidateMemory asks for a consolidation of the conversation and audits
// it as memory.consolidate. It is refused as memory.ConsolidateAllowed
// decides: 409 MEMORY_DISABLED while memory is off and 409 PAUSED while the
// conversation is paused.
func (s *Server) consolidateMemory(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.memoryConversation(w, r)
	if !ok {
		return
	}
	pv, err := s.d.Policies.Current(r.Context(), conv.Key)
	if err == nil {
		err = memory.ConsolidateAllowed(pv.Policy)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.d.Memory != nil {
		s.d.Memory.Trigger(conv.Key)
	}
	s.audit(r, sess, conv.BotInstanceID, "memory.consolidate", conv.Key, struct{}{})
	s.writeJSON(w, http.StatusAccepted, struct {
		Accepted bool `json:"accepted"`
	}{true})
}
