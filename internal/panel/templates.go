package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// maxImportBytes bounds the body of a persona template import.
const maxImportBytes = 64 << 20

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

// templateUsers maps each persona template to the bots whose private or
// group template it is, each bot once per template.
func (s *Server) templateUsers() map[string][]templateUser {
	out := map[string][]templateUser{}
	for _, b := range s.d.Bots.All() {
		out[b.PrivateTemplate] = append(out[b.PrivateTemplate], s.templateUser(b))
		if b.GroupTemplate != b.PrivateTemplate {
			out[b.GroupTemplate] = append(out[b.GroupTemplate], s.templateUser(b))
		}
	}
	return out
}

// templateUser is b as a bot whose new chats start from its persona template.
func (s *Server) templateUser(b config.BotConfig) templateUser {
	return templateUser{ID: b.ID, Name: b.Name, Source: s.botSource(b.ID)}
}

// botsByID returns the bots of the directory with the IDs ids, in their
// order, leaving out an ID no bot has any more.
func (s *Server) botsByID(ids []string) []templateUser {
	out := make([]templateUser, 0, len(ids))
	for _, id := range ids {
		if b, ok := s.d.Bots.Bot(id); ok {
			out = append(out, s.templateUser(b))
		}
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

// deleteTemplate deletes the template {name}, also while bots use it, and
// answers the bots that did (see persona.Service.DeleteTemplate), as the
// list's users, which use "default" from now on, each noting the problem;
// "default" is 403.
func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request, sess *Session) {
	name := r.PathValue("name")
	ids, err := s.d.Personas.DeleteTemplate(r.Context(), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	affected := s.botsByID(ids)
	s.templatesChanged(r)
	s.audit(r, sess, "", "persona.template.delete", name, struct {
		AffectedBots []templateUser `json:"affected_bots"`
	}{affected})
	s.writeJSON(w, http.StatusOK, struct {
		Name         string         `json:"name"`
		AffectedBots []templateUser `json:"affected_bots"`
	}{name, affected})
}

// getBuiltinTemplate answers the built-in default persona: "default" while
// none is stored, and where a new blank template starts.
func (s *Server) getBuiltinTemplate(w http.ResponseWriter, _ *http.Request, _ *Session) {
	s.writeJSON(w, http.StatusOK, struct {
		Persona conloomapi.PersonaDoc `json:"persona"`
	}{conloomapi.DefaultPersona()})
}

// exportTemplate downloads the persona template {name} as an exchange file
// named persona-{name}.json; an unknown name is 404.
func (s *Server) exportTemplate(w http.ResponseWriter, r *http.Request, sess *Session) {
	name := r.PathValue("name")
	s.exportTemplates(w, r, sess, []string{name}, name, "persona-"+name+".json")
}

// exportAllTemplates downloads every persona template of the catalog as an
// exchange file named persona-templates-YYYYMMDD.json, dated in the server's
// time zone.
func (s *Server) exportAllTemplates(w http.ResponseWriter, r *http.Request, sess *Session) {
	s.exportTemplates(w, r, sess, nil, "", "persona-templates-"+s.now().In(s.location()).Format("20060102")+".json")
}

// exportTemplates answers the persona templates names, all of them by name
// when none, as an exchange file (see persona.Service.Export) to save as
// filename, and audits the export for target with the names to export. An
// unknown name is 404. The file is the exchange file indented by two spaces,
// written one template at a time so that at most one appearance image is in
// memory. The first template is read before anything is written, so that its
// failure is an error response; a template that fails after the file has
// started aborts the response. Exporting them all, a template deleted after
// the names were listed is left out. HEAD only answers the headers, or 404
// for an unknown name: it neither builds the file nor audits an export.
func (s *Server) exportTemplates(w http.ResponseWriter, r *http.Request, sess *Session, names []string, target, filename string) {
	all := names == nil
	if all {
		for _, info := range s.d.Personas.Templates() {
			names = append(names, info.Name)
		}
	}
	for _, name := range names {
		if _, ok := s.d.Personas.Template(name); !ok {
			s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, "not found")
			return
		}
	}
	if r.Method == http.MethodHead {
		writeDownloadHeaders(w, filename)
		return
	}
	ctx := r.Context()
	deleted := func(err error) bool { return all && errors.Is(err, store.ErrNotFound) }
	var first []byte
	for len(names) > 0 && first == nil {
		data, err := s.exportedTemplate(ctx, names[0])
		switch {
		case deleted(err):
			names = names[1:]
		case err != nil:
			s.fail(w, r, err)
			return
		default:
			first = data
		}
	}
	s.audit(r, sess, "", "persona.template.export", target, struct {
		Templates []string `json:"templates"`
	}{names})
	writeDownloadHeaders(w, filename)
	format, _ := json.Marshal(persona.ExchangeFormat)
	exportedAt, _ := json.Marshal(s.now().UTC().Truncate(time.Second))
	if _, err := fmt.Fprintf(w, "{\n  \"format\": %s,\n  \"version\": %d,\n  \"exported_at\": %s,\n  \"templates\": [",
		format, persona.ExchangeVersion, exportedAt); err != nil {
		return
	}
	written := 0
	for i, name := range names {
		data := first
		if i > 0 {
			var err error
			if data, err = s.exportedTemplate(ctx, name); err != nil {
				if deleted(err) {
					continue
				}
				if ctx.Err() == nil {
					s.log.Error("persona template export aborted", "trace_id", traceID(ctx), "template", name, "error", err)
				}
				panic(http.ErrAbortHandler)
			}
		}
		sep := "\n    "
		if written > 0 {
			sep = ",\n    "
		}
		if _, err := io.WriteString(w, sep); err != nil {
			return
		}
		if _, err := w.Write(data); err != nil {
			return
		}
		written++
	}
	end := "\n  ]\n}\n"
	if written == 0 {
		end = "]\n}\n"
	}
	_, _ = io.WriteString(w, end)
}

// exportedTemplate returns the persona template name as Export exports it,
// with its appearance image, as JSON indented as an item of the templates of
// the exchange file exportTemplates writes.
func (s *Server) exportedTemplate(ctx context.Context, name string) ([]byte, error) {
	f, err := s.d.Personas.Export(ctx, []string{name})
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(f.Templates[0], "    ", "  ")
}

// writeDownloadHeaders answers 200 with the headers of a JSON file to save as
// filename.
func writeDownloadHeaders(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", attachment(filename))
	w.WriteHeader(http.StatusOK)
}

// attachment is the Content-Disposition of a download saved as filename. A
// name that is not plain printable ASCII, as a template migrated from an old
// config file may have, is encoded as RFC 2231 says.
func attachment(filename string) string {
	for _, b := range []byte(filename) {
		if b < ' ' || b > '~' || b == '"' || b == '\\' {
			return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
		}
	}
	return `attachment; filename="` + filename + `"`
}

// importedTemplate is a persona template an import stored: Requested is its
// name in the file, Name the name it was stored under, and Created false when
// it replaced a stored template.
type importedTemplate struct {
	Name      string `json:"name"`
	Requested string `json:"requested"`
	Created   bool   `json:"created"`
}

// importTemplates stores the persona templates of the request body, an
// exchange file as exported, all of them or none (see
// persona.Service.Import), and answers, in the order of the file, the name
// each one was stored under, its name in the file and whether it was created.
// on_conflict, fail by default, overwrite or rename, decides what happens to
// a template whose name a stored template has; with fail, such names are 409
// TEMPLATE_EXISTS listing them. A body over 64 MiB is 413; one that is not one
// JSON value of known fields, or has more than persona.MaxExchangeTemplates
// templates, and a file Import refuses, are 400. Before it reads the body,
// the import takes the image upload slot, and it holds it until Import has
// returned, so at most one import file is in memory: while another upload or
// import holds it, the request is a retryable 409 CHAT_BUSY, its body unread.
// The import is audited with the names, never the personas or images.
func (s *Server) importTemplates(w http.ResponseWriter, r *http.Request, sess *Session) {
	release, ok := s.tryImageSlot(w, r, s.imageUploads)
	if !ok {
		return
	}
	defer release()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBytes))
	if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
		s.writeError(w, r, http.StatusRequestEntityTooLarge, conloomapi.CodeInvalidEvent,
			fmt.Sprintf("导入文件不能超过 %d MiB", maxImportBytes>>20))
		return
	}
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "could not read the request body", err)
		return
	}
	var f persona.ExchangeFile
	err = decodeJSON(bytes.NewReader(data), &f)
	if errors.Is(err, persona.ErrTooManyTemplates) {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			fmt.Sprintf("一个文件最多 %d 个模板", persona.MaxExchangeTemplates))
		return
	}
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "invalid JSON body: "+err.Error())
		return
	}
	mode := persona.ConflictMode(r.URL.Query().Get("on_conflict"))
	if mode == "" {
		mode = persona.ConflictFail
	}
	results, err := s.d.Personas.Import(r.Context(), f, mode, panelActor(sess))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]importedTemplate, len(results))
	detail := struct {
		OnConflict  string            `json:"on_conflict"`
		Created     []string          `json:"created"`
		Overwritten []string          `json:"overwritten"`
		Renamed     map[string]string `json:"renamed,omitempty"`
	}{OnConflict: string(mode), Created: []string{}, Overwritten: []string{}}
	for i, res := range results {
		out[i] = importedTemplate{Name: res.Name, Requested: res.Requested, Created: res.Created}
		if res.Created {
			detail.Created = append(detail.Created, res.Name)
		} else {
			detail.Overwritten = append(detail.Overwritten, res.Name)
		}
		if res.Name != res.Requested {
			if detail.Renamed == nil {
				detail.Renamed = map[string]string{}
			}
			detail.Renamed[res.Requested] = res.Name
		}
	}
	s.audit(r, sess, "", "persona.template.import", "", detail)
	s.writeJSON(w, http.StatusOK, struct {
		Templates []importedTemplate `json:"templates"`
	}{out})
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
