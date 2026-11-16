package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// maxBodyBytes bounds request bodies.
const maxBodyBytes = 1 << 20

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError logs the failure with the request's trace ID (at error level for
// 5xx) and writes the error body. 429, 503, 504 and CHAT_BUSY are marked
// retryable.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	s.respondError(w, r, status, code, message, nil)
}

// badRequest writes 400 INVALID_EVENT with err's text.
func (s *Server) badRequest(w http.ResponseWriter, r *http.Request, err error) {
	s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, err.Error())
}

// respondError is writeError that also logs cause, which is never sent.
func (s *Server) respondError(w http.ResponseWriter, r *http.Request, status int, code, message string, cause error) {
	s.respond(w, r, status, code, message, cause, status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout || code == conloomapi.CodeChatBusy, nil)
}

// apiError is conloomapi.ErrorBody with Names, the names an error is about
// when it has a list of them, such as the templates of a refused import.
type apiError struct {
	conloomapi.ErrorBody
	Names []string `json:"names,omitempty"`
}

// respond is respondError with the error's retryable flag given by the
// caller, and names, when not empty, listed in the body as names.
func (s *Server) respond(w http.ResponseWriter, r *http.Request, status int, code, message string, cause error, retryable bool,
	names []string) {
	level := slog.LevelDebug
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	attrs := []any{"trace_id", traceID(r.Context()), "method", r.Method, "path", r.URL.Path, "status", status, "code", code}
	if cause != nil {
		attrs = append(attrs, "error", cause)
	}
	s.log.Log(r.Context(), level, "panel request failed", attrs...)
	s.writeJSON(w, status, struct {
		Error apiError `json:"error"`
	}{apiError{conloomapi.ErrorBody{Code: code, Message: message, TraceID: traceID(r.Context()), Retryable: retryable}, names}})
}

// fail maps err from a collaborator to an error response as the Bot
// Interaction API does: store.ErrNotFound is 404; a rejected policy, persona,
// persona template, member memory, bar or panel bot edit is 400 with the
// error text, and so is a request for the hidden state of a conversation that
// is not private; a change to a bot of the config file, and deleting the
// persona template "default", is 403; a refused consolidation is 409
// MEMORY_DISABLED or PAUSED; importing templates whose names stored
// templates have is 409 TEMPLATE_EXISTS listing them in names; a bot being
// deleted, or a chat whose cancelled tasks are still stopping, is a retryable
// 409 CHAT_BUSY; a cancelled request or a member memory that kept changing is
// a retryable 503; a description without a vision model is a 503
// MODEL_UNAVAILABLE that is not retryable, as only a config change helps;
// anything else is a 500 whose details are only logged.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if ctxErr := r.Context().Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		s.respondError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "request cancelled; server may be shutting down", err)
		return
	}
	var conflict *persona.ImportConflictError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.respondError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, "not found", err)
	case errors.Is(err, policy.ErrInvalid), errors.Is(err, persona.ErrInvalid):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, err.Error(), err)
	case errors.Is(err, memory.ErrInvalid):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			strings.TrimPrefix(err.Error(), memory.ErrInvalid.Error()+": "), err)
	case errors.Is(err, core.ErrInvalidBar):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			strings.TrimPrefix(err.Error(), core.ErrInvalidBar.Error()+": "), err)
	case errors.Is(err, core.ErrInvalidBot):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			strings.TrimPrefix(err.Error(), core.ErrInvalidBot.Error()+": "), err)
	case errors.Is(err, core.ErrConfigBot):
		s.respondError(w, r, http.StatusForbidden, conloomapi.CodeForbidden, strings.TrimPrefix(core.ErrConfigBot.Error(), "core: "), err)
	case errors.Is(err, persona.ErrDefaultTemplate):
		s.respondError(w, r, http.StatusForbidden, conloomapi.CodeForbidden, strings.TrimPrefix(err.Error(), "persona: "), err)
	case errors.As(err, &conflict):
		s.respond(w, r, http.StatusConflict, conloomapi.CodeTemplateExists, strings.TrimPrefix(conflict.Error(), "persona: "), err,
			false, conflict.Names)
	case errors.Is(err, core.ErrNoVision):
		s.respond(w, r, http.StatusServiceUnavailable, conloomapi.CodeModelUnavailable, "未配置识图模型", err, false, nil)
	case errors.Is(err, core.ErrChatBusy):
		s.respondError(w, r, http.StatusConflict, conloomapi.CodeChatBusy, strings.TrimPrefix(core.ErrChatBusy.Error(), "core: "), err)
	case errors.Is(err, core.ErrBotDeleting):
		s.respondError(w, r, http.StatusConflict, conloomapi.CodeChatBusy, strings.TrimPrefix(core.ErrBotDeleting.Error(), "core: "), err)
	case errors.Is(err, core.ErrNotPrivate):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "not a private conversation", err)
	case errors.Is(err, memory.ErrUpdateConflict):
		s.respondError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "member memory kept changing; try again", err)
	case errors.Is(err, memory.ErrMemoryDisabled):
		s.respondError(w, r, http.StatusConflict, conloomapi.CodeMemoryDisabled, memory.ErrMemoryDisabled.Error(), err)
	case errors.Is(err, memory.ErrPaused):
		s.respondError(w, r, http.StatusConflict, conloomapi.CodePaused, memory.ErrPaused.Error(), err)
	default:
		s.respondError(w, r, http.StatusInternalServerError, conloomapi.CodeInternal, "internal error", err)
	}
}

// decode decodes the JSON request body into v, writing a 400 response itself
// when the body exceeds 1 MiB, is not one JSON value or has unknown fields.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	err := decodeJSON(http.MaxBytesReader(w, r.Body, maxBodyBytes), v)
	if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "request body exceeds 1 MiB")
		return false
	}
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// decodeJSON decodes body, which must be one JSON value without unknown
// fields, into v. It returns the error of reading body as it is.
func decodeJSON(body io.Reader, v any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		var extra json.RawMessage
		switch err = dec.Decode(&extra); err {
		case io.EOF:
			err = nil
		case nil:
			err = errors.New("trailing data after the JSON value")
		}
	}
	return err
}

// panelActor is who a change made through the panel by sess's user is
// recorded as.
func panelActor(sess *Session) string { return "panel:" + sess.User }

// audit records a change made through the panel by sess's user. A failed
// write is only logged: the change is already stored.
func (s *Server) audit(r *http.Request, sess *Session, botID, kind, target string, detail any) {
	b, err := json.Marshal(detail)
	if err != nil {
		s.log.Warn("panel audit detail not encodable", "trace_id", traceID(r.Context()), "kind", kind, "error", err)
		b = []byte("null")
	}
	err = s.d.Audit.InsertAudit(context.WithoutCancel(r.Context()), store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: botID, Kind: kind, Actor: panelActor(sess), Target: target,
		Detail: string(b), CreatedAt: s.now().UTC(),
	})
	if err != nil {
		s.log.Warn("panel change made but audit failed", "trace_id", traceID(r.Context()), "kind", kind, "error", err)
	}
}
