package panel

import (
	"errors"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// policyVersionView is a policy version; CreatedAt is null for the defaults
// of a conversation without a stored policy (version 0).
type policyVersionView struct {
	Version   int                    `json:"version"`
	Policy    conloomapi.GroupPolicy `json:"policy"`
	Reason    string                 `json:"reason"`
	UpdatedBy string                 `json:"updated_by"`
	CreatedAt *time.Time             `json:"created_at"`
}

func newPolicyVersionView(pv store.PolicyVersion) policyVersionView {
	return policyVersionView{
		Version: pv.Version, Policy: pv.Policy, Reason: pv.Reason, UpdatedBy: pv.UpdatedBy, CreatedAt: nullTime(pv.CreatedAt),
	}
}

type personaVersionView struct {
	Version   int                   `json:"version"`
	Template  string                `json:"template"`
	Persona   conloomapi.PersonaDoc `json:"persona"`
	Reason    string                `json:"reason"`
	UpdatedBy string                `json:"updated_by"`
	CreatedAt time.Time             `json:"created_at"`
}

func newPersonaVersionView(pv store.PersonaVersion) personaVersionView {
	return personaVersionView{
		Version: pv.Version, Template: pv.TemplateName, Persona: pv.Persona, Reason: pv.Reason,
		UpdatedBy: pv.UpdatedBy, CreatedAt: pv.CreatedAt.UTC(),
	}
}

// getPolicy returns the current policy, the defaults as version 0 when none is
// stored (policy.Service.Current stores nothing), and the stored versions
// newest first.
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
	s.writeJSON(w, http.StatusOK, struct {
		Current  policyVersionView   `json:"current"`
		Versions []policyVersionView `json:"versions"`
	}{newPolicyVersionView(cur), out})
}

type personaResponse struct {
	Current         *personaVersionView    `json:"current"`
	PendingTemplate *string                `json:"pending_template"`
	TemplateDoc     *conloomapi.PersonaDoc `json:"template_doc"`
	Versions        []personaVersionView   `json:"versions"`
	Templates       []string               `json:"templates"`
}

// getPersona returns the current persona and its versions newest first. It
// never writes: a conversation without a persona gets current null, and the
// template its first version will be made from (the bot's persona_template,
// "default" when unset) with that template's content.
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
		if bot, ok := s.configuredBot(conv.BotInstanceID); ok && bot.PersonaTemplate != "" {
			name = bot.PersonaTemplate
		}
		resp.PendingTemplate = &name
		if doc, ok := s.d.Config.PersonaTemplates[name]; ok {
			resp.TemplateDoc = &doc
		} else if name == "default" {
			doc := conloomapi.DefaultPersona()
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
	s.writeJSON(w, http.StatusOK, resp)
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
// the summary's without user_id.
func (s *Server) listMemoryRevisions(w http.ResponseWriter, r *http.Request, _ *Session) {
	conv, ok := s.memoryConversation(w, r)
	if !ok {
		return
	}
	revs, err := s.d.Store.ListMemoryRevisions(r.Context(), conv.Key, r.URL.Query().Get("user_id"))
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
