package panel

import (
	"context"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// templateUser is a bot whose new chats start from a persona template.
type templateUser struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// templateSummary is a persona template of the catalog as the templates page
// lists it. Source is persona.TemplateSource; Users are the bots whose
// persona template it is, as it takes effect; UpdatedBy and UpdatedAt are of
// the last write, empty and null for the built-in default.
type templateSummary struct {
	Name            string         `json:"name"`
	Source          string         `json:"source"`
	PersonaName     string         `json:"persona_name"`
	CharacterName   string         `json:"character_name"`
	AppearanceImage string         `json:"appearance_image"`
	Users           []templateUser `json:"users"`
	UpdatedBy       string         `json:"updated_by"`
	UpdatedAt       *time.Time     `json:"updated_at"`
}

// templateView is a persona template with its persona.
type templateView struct {
	templateSummary
	Persona conloomapi.PersonaDoc `json:"persona"`
}

// templateUsers maps each persona template to the bots whose template it is.
func (s *Server) templateUsers() map[string][]templateUser {
	out := map[string][]templateUser{}
	for _, b := range s.d.Bots.All() {
		out[b.PersonaTemplate] = append(out[b.PersonaTemplate], templateUser{ID: b.ID, Name: b.Name, Source: s.botSource(b.ID)})
	}
	return out
}

func summarizeTemplate(info persona.TemplateInfo, users []templateUser) templateSummary {
	if users == nil {
		users = []templateUser{}
	}
	return templateSummary{
		Name: info.Name, Source: string(info.Source), PersonaName: info.Doc.PersonaName, CharacterName: info.Doc.CharacterName,
		AppearanceImage: info.Doc.AppearanceImage, Users: users, UpdatedBy: info.UpdatedBy, UpdatedAt: nullTime(info.UpdatedAt),
	}
}

// templateInfo returns the catalog's persona template name.
func (s *Server) templateInfo(name string) (persona.TemplateInfo, bool) {
	for _, info := range s.d.Personas.Templates() {
		if info.Name == name {
			return info, true
		}
	}
	return persona.TemplateInfo{}, false
}

// writeTemplate writes the persona template name with status, or 404 when the
// catalog has none.
func (s *Server) writeTemplate(w http.ResponseWriter, r *http.Request, status int, name string) {
	info, ok := s.templateInfo(name)
	if !ok {
		s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, "not found")
		return
	}
	s.writeJSON(w, status, templateView{
		templateSummary: summarizeTemplate(info, s.templateUsers()[name]), Persona: info.Doc,
	})
}

// listTemplates lists the persona templates of the catalog by name.
func (s *Server) listTemplates(w http.ResponseWriter, _ *http.Request, _ *Session) {
	users := s.templateUsers()
	infos := s.d.Personas.Templates()
	out := make([]templateSummary, 0, len(infos))
	for _, info := range infos {
		out = append(out, summarizeTemplate(info, users[info.Name]))
	}
	s.writeJSON(w, http.StatusOK, struct {
		Templates []templateSummary `json:"templates"`
	}{out})
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request, _ *Session) {
	s.writeTemplate(w, r, http.StatusOK, r.PathValue("name"))
}

// putTemplate stores the request's persona as the template {name}, which
// replaces the template of that name or, for a new name, creates one (201).
func (s *Server) putTemplate(w http.ResponseWriter, r *http.Request, sess *Session) {
	var req struct {
		Persona *conloomapi.PersonaDoc `json:"persona"`
		Reason  string                 `json:"reason"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.Persona == nil {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "persona is required")
		return
	}
	name := r.PathValue("name")
	_, existed := s.d.Personas.Template(name)
	if err := s.d.Personas.PutTemplate(r.Context(), name, *req.Persona, panelActor(sess)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.templatesChanged(r)
	info, _ := s.templateInfo(name)
	s.audit(r, sess, "", "persona.template.put", name, struct {
		Source  string `json:"source"`
		Created bool   `json:"created"`
		Reason  string `json:"reason,omitempty"`
	}{string(info.Source), !existed, req.Reason})
	status := http.StatusOK
	if !existed {
		status = http.StatusCreated
	}
	s.writeTemplate(w, r, status, name)
}

// resetTemplate deletes the stored "default", which brings back the built-in
// default persona; any other {name} is 400.
func (s *Server) resetTemplate(w http.ResponseWriter, r *http.Request, sess *Session) {
	name := r.PathValue("name")
	if err := s.d.Personas.ResetTemplate(r.Context(), name); err != nil {
		s.fail(w, r, err)
		return
	}
	s.templatesChanged(r)
	s.audit(r, sess, "", "persona.template.reset", name, struct{}{})
	s.writeTemplate(w, r, http.StatusOK, name)
}

// deleteTemplate deletes the template {name}; "default" is 403.
func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request, sess *Session) {
	name := r.PathValue("name")
	if err := s.d.Personas.DeleteTemplate(r.Context(), name); err != nil {
		s.fail(w, r, err)
		return
	}
	s.templatesChanged(r)
	s.audit(r, sess, "", "persona.template.delete", name, struct{}{})
	w.WriteHeader(http.StatusNoContent)
}

// templatesChanged publishes the bots again after a persona template changed,
// so that a bot whose template is back loses its problem. A failure is only
// logged: the template change is already stored.
func (s *Server) templatesChanged(r *http.Request) {
	if s.d.PanelBots == nil {
		return
	}
	if err := s.d.PanelBots.Load(context.WithoutCancel(r.Context())); err != nil {
		s.log.Warn("persona template changed but panel bots not refreshed", "trace_id", traceID(r.Context()), "error", err)
	}
}
