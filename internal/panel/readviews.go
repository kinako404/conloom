package panel

import (
	"errors"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// getPolicy returns the current policy, the defaults as version 0 when none is
// stored (policy.Service.Current stores nothing), the stored versions newest
// first and the memory limits in effect, under the policy the conversation's
// memory follows (see memoryPolicy).
func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request, _ *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	cur, err := s.d.Policies.Current(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	versions, err := s.d.Store.ListPolicyVersions(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]policyVersionView, 0, len(versions))
	for _, pv := range versions {
		out = append(out, newPolicyVersionView(pv))
	}
	pol, err := s.memoryPolicy(r.Context(), conv, cur.Policy)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	mp := pol.Memory
	s.writeJSON(w, http.StatusOK, struct {
		Current      policyVersionView   `json:"current"`
		Versions     []policyVersionView `json:"versions"`
		MemoryLimits memoryLimitsView    `json:"memory_limits"`
	}{newPolicyVersionView(cur), out, memoryLimitsView{
		SummaryMaxChars: memory.SummaryLimit(mp, conv.Type), NoteMaxChars: memory.NoteLimit(mp, conv.Type),
		DefaultSummaryMaxChars: memory.SummaryLimit(conloomapi.MemoryPolicy{}, conv.Type),
		DefaultNoteMaxChars:    memory.NoteLimit(conloomapi.MemoryPolicy{}, conv.Type),
	}})
}

// memoryLimitsView is a conversation's summary and note limits: those in
// effect under the policy its memory follows and its type's defaults, which a
// policy value of 0 stands for.
type memoryLimitsView struct {
	SummaryMaxChars        int `json:"summary_max_chars"`
	NoteMaxChars           int `json:"note_max_chars"`
	DefaultSummaryMaxChars int `json:"default_summary_max_chars"`
	DefaultNoteMaxChars    int `json:"default_note_max_chars"`
}

type personaResponse struct {
	Current         *personaVersionView    `json:"current"`
	PendingTemplate *string                `json:"pending_template"`
	TemplateDoc     *conloomapi.PersonaDoc `json:"template_doc"`
	Versions        []personaVersionView   `json:"versions"`
	Templates       []string               `json:"templates"`
	Minor           bool                   `json:"minor"`
	ContentMode     conloomapi.ContentMode `json:"content_mode"`
}

// getPersona returns the current persona and its versions newest first. It
// never writes: a conversation without a persona gets current null, and the
// template its first version will be made from (the bot's template for the
// conversation's type, "default" when unset) with that template's content in
// the catalog, online
// changes included. minor and content_mode describe the current persona, or
// that template without one; content_mode is the mode it has in a private
// chat, whatever the conversation's type.
func (s *Server) getPersona(w http.ResponseWriter, r *http.Request, _ *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	resp := personaResponse{Templates: s.d.Personas.TemplateNames()}
	pv, err := s.d.Store.LatestPersona(r.Context(), conv.Key)
	switch {
	case err == nil:
		cur := newPersonaVersionView(pv)
		resp.Current = &cur
	case errors.Is(err, store.ErrNotFound):
		name := "default"
		if bot, ok := s.d.Bots.Bot(conv.BotInstanceID); ok && bot.TemplateFor(conv.Type) != "" {
			name = bot.TemplateFor(conv.Type)
		}
		resp.PendingTemplate = &name
		if doc, ok := s.d.Personas.Template(name); ok {
			resp.TemplateDoc = &doc
		}
	default:
		s.fail(w, r, err)
		return
	}
	versions, err := s.d.Store.ListPersonaVersions(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp.Versions = make([]personaVersionView, 0, len(versions))
	for _, v := range versions {
		resp.Versions = append(resp.Versions, newPersonaVersionView(v))
	}
	var doc conloomapi.PersonaDoc
	if resp.Current != nil {
		doc = resp.Current.Persona
	} else if resp.TemplateDoc != nil {
		doc = *resp.TemplateDoc
	}
	resp.Minor = doc.Minor
	resp.ContentMode = conloomapi.ModeFor(doc, conloomapi.ConvPrivate)
	s.writeJSON(w, http.StatusOK, resp)
}

// getAffinityBounds lists the affinity tiers from the lowest (tier i starts
// at affinity i*20, see memory.TierIndex) and, for each, the built-in
// boundary of the open-mode relationship hint (core.DefaultAffinityBound),
// which a persona's intimacy bound for the tier replaces.
func (s *Server) getAffinityBounds(w http.ResponseWriter, _ *http.Request, _ *Session) {
	n := len(conloomapi.DefaultIntimacyLevels())
	tiers, defaults := make([]string, n), make([]string, n)
	for i := range n {
		tiers[i] = memory.TierName(i * 20)
		defaults[i] = core.DefaultAffinityBound(tiers[i])
	}
	s.writeJSON(w, http.StatusOK, struct {
		Tiers    []string `json:"tiers"`
		Defaults []string `json:"defaults"`
	}{tiers, defaults})
}

// getMemory returns the same JSON as the Bot Interaction API's GET
// /api/v1/conversations/{id}/memory.
func (s *Server) getMemory(w http.ResponseWriter, r *http.Request, _ *Session) {
	conv, ok := s.memoryConversation(w, r)
	if !ok {
		return
	}
	state, err := s.d.MemoryAdmin.View(r.Context(), conv)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, state)
}

type memoryRevisionView struct {
	ID        int64     `json:"id"`
	UserID    string    `json:"user_id"`
	Field     string    `json:"field"`
	Old       string    `json:"old"`
	New       string    `json:"new"`
	Source    string    `json:"source"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// listMemoryRevisions returns the user_id member's revisions oldest first, or
// the summary's without user_id, in the conversation's memory scope
// (memory.ScopeKey).
func (s *Server) listMemoryRevisions(w http.ResponseWriter, r *http.Request, _ *Session) {
	conv, ok := s.memoryConversation(w, r)
	if !ok {
		return
	}
	revs, err := s.d.Store.ListMemoryRevisions(r.Context(), memory.ScopeKey(conv), r.URL.Query().Get("user_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]memoryRevisionView, 0, len(revs))
	for _, rv := range revs {
		out = append(out, memoryRevisionView{
			ID: rv.ID, UserID: rv.PlatformUserID, Field: rv.Field, Old: rv.OldValue, New: rv.NewValue,
			Source: rv.Source, Reason: rv.Reason, CreatedAt: rv.CreatedAt.UTC(),
		})
	}
	s.writeJSON(w, http.StatusOK, struct {
		Revisions []memoryRevisionView `json:"revisions"`
	}{out})
}
