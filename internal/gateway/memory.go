package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// memoryConversation resolves the {id} path value to a group conversation of
// the token's bot, reporting an error response itself on failure.
func (s *Server) memoryConversation(w http.ResponseWriter, r *http.Request, tok *token) (store.ConversationRecord, bool) {
	conv, ok := s.conversation(w, r, tok)
	if !ok {
		return conv, false
	}
	if conv.Type != conloomapi.ConvGroup {
		s.invalid(w, r, "memory is only kept for group conversations")
		return conv, false
	}
	return conv, true
}

// memoryEnabled reports whether memory is in effect for the conversation.
func (s *Server) memoryEnabled(ctx context.Context, convKey string) (bool, error) {
	pv, err := s.d.Policies.Current(ctx, convKey)
	if err != nil {
		return false, err
	}
	return pv.Policy.Memory.Enabled && pv.Policy.Concurrency.AllowLongRequests, nil
}

func memberState(m store.MemberMemory) conloomapi.MemberMemoryState {
	st := conloomapi.MemberMemoryState{
		UserID: m.PlatformUserID, Name: m.DisplayName, Note: m.Note, UpdatedAt: m.UpdatedAt,
	}
	if m.Affinity != nil {
		v := *m.Affinity
		st.Affinity, st.Tier = &v, memory.TierName(v)
	}
	return st
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.memoryConversation(w, r, tok)
	if !ok {
		return
	}
	enabled, err := s.memoryEnabled(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	g, err := s.d.Store.GetGroupMemory(r.Context(), conv.Key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	members, err := s.d.Store.ListMemberMemories(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	state := conloomapi.MemoryState{
		ConversationID: conv.ID, Enabled: enabled, Summary: g.Summary, CursorSeq: g.CursorSeq,
		LastError: g.LastError, Members: []conloomapi.MemberMemoryState{},
	}
	if !g.LastRunAt.IsZero() {
		at := g.LastRunAt
		state.LastRunAt = &at
	}
	for _, m := range members {
		if m.Note != "" || m.Affinity != nil {
			state.Members = append(state.Members, memberState(m))
		}
	}
	writeJSON(w, http.StatusOK, state)
}

// putMemberMemory sets a member's note, affinity or both, creating the
// member's row if needed. It works whether or not memory is on, so admins can
// correct frozen data.
func (s *Server) putMemberMemory(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.memoryConversation(w, r, tok)
	if !ok {
		return
	}
	var put conloomapi.MemberMemoryPut
	if !s.decodeBody(w, r, &put) {
		return
	}
	if put.Note == nil && put.Affinity == nil {
		s.invalid(w, r, "at least one of note and affinity is required")
		return
	}
	var note string
	if put.Note != nil {
		note = strings.TrimSpace(*put.Note)
		if n := utf8.RuneCountInString(note); n > memory.NoteMaxRunes {
			s.invalid(w, r, "note must be at most "+strconv.Itoa(memory.NoteMaxRunes)+" characters, got "+strconv.Itoa(n))
			return
		}
	}
	if put.Affinity != nil && memory.Clamp(*put.Affinity) != *put.Affinity {
		s.invalid(w, r, "affinity must be in [0, 100], got "+strconv.Itoa(*put.Affinity))
		return
	}
	userID := r.PathValue("user_id")
	upd, err := memory.UpdateMember(r.Context(), s.d.Store, conv.Key, userID, store.RevisionAPI,
		func(m *store.MemberMemory) (string, bool) {
			changed := false
			if put.Note != nil && m.Note != note {
				m.Note, changed = note, true
			}
			if a := put.Affinity; a != nil && (m.Affinity == nil || *m.Affinity != *a) {
				v := *a
				m.Affinity, changed = &v, true
			}
			return put.Reason, changed
		})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if upd.Written {
		if upd.AffinityChanged() && s.d.Counters != nil {
			s.d.Counters.MemoryAffinityChanges.WithLabelValues(store.RevisionAPI).Inc()
		}
		var fields []string
		if upd.Before.Note != upd.After.Note {
			fields = append(fields, store.FieldNote)
		}
		if upd.AffinityChanged() {
			fields = append(fields, store.FieldAffinity)
		}
		s.memoryAudit(r, tok, conv.Key, "memory.update", struct {
			UserID string   `json:"user_id"`
			Fields []string `json:"fields"`
		}{userID, fields})
	}
	got, err := s.d.Store.GetMemberMemories(r.Context(), conv.Key, []string{userID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	m, ok := got[userID]
	if !ok {
		m = upd.After
	}
	writeJSON(w, http.StatusOK, memberState(m))
}

// deleteMemberMemory forgets a member as /忘记我 does. It works whether or not
// memory is on.
func (s *Server) deleteMemberMemory(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.memoryConversation(w, r, tok)
	if !ok {
		return
	}
	userID := r.PathValue("user_id")
	before, err := s.d.Store.GetMemberMemories(r.Context(), conv.Key, []string{userID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.d.Store.ForgetMember(r.Context(), conv.Key, userID, time.Now()); err != nil {
		s.fail(w, r, err)
		return
	}
	if before[userID].Affinity != nil && s.d.Counters != nil {
		s.d.Counters.MemoryAffinityChanges.WithLabelValues(store.RevisionAPI).Inc()
	}
	s.memoryAudit(r, tok, conv.Key, "memory.forget", struct {
		UserID string `json:"user_id"`
	}{userID})
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
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

// memoryAudit records a memory change made by tok. A failed audit write is
// only logged: the change is already stored.
func (s *Server) memoryAudit(r *http.Request, tok *token, convKey, kind string, detail any) {
	b, _ := json.Marshal(detail)
	err := s.d.Store.InsertAudit(r.Context(), store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: tok.botID, Kind: kind, Actor: tok.name, Target: convKey,
		Detail: string(b), CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		s.log.Warn("memory changed but audit failed", "trace_id", traceID(r.Context()), "path", r.URL.Path, "error", err)
	}
}
