package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

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
// 5xx) and writes the error body. 429 and 503 are marked retryable.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	s.respondError(w, r, status, code, message, nil)
}

// respondError is writeError that also logs cause, which is never sent.
func (s *Server) respondError(w http.ResponseWriter, r *http.Request, status int, code, message string, cause error) {
	level := slog.LevelDebug
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	attrs := []any{"trace_id", traceID(r.Context()), "method", r.Method, "path", r.URL.Path, "status", status, "code", code}
	if cause != nil {
		attrs = append(attrs, "error", cause)
	}
	s.log.Log(r.Context(), level, "panel request failed", attrs...)
	s.writeJSON(w, status, conloomapi.ErrorResponse{Error: conloomapi.ErrorBody{
		Code: code, Message: message, TraceID: traceID(r.Context()),
		Retryable: status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable,
	}})
}

// fail maps err from a collaborator to an error response as the Bot
// Interaction API does: store.ErrNotFound is 404; a rejected policy, persona
// or member memory edit is 400 with the error text; a refused consolidation
// is 409 MEMORY_DISABLED or PAUSED; a cancelled request or a member memory
// that kept changing is a retryable 503; anything else is a 500 whose details
// are only logged.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if ctxErr := r.Context().Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		s.respondError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "request cancelled; server may be shutting down", err)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.respondError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, "not found", err)
	case errors.Is(err, policy.ErrInvalid), errors.Is(err, persona.ErrInvalid):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, err.Error(), err)
	case errors.Is(err, memory.ErrInvalid):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			strings.TrimPrefix(err.Error(), memory.ErrInvalid.Error()+": "), err)
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
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
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

// audit records a change made through the panel by sess's user. A failed
// write is only logged: the change is already stored.
func (s *Server) audit(r *http.Request, sess *Session, botID, kind, target string, detail any) {
	b, err := json.Marshal(detail)
	if err != nil {
		s.log.Warn("panel audit detail not encodable", "trace_id", traceID(r.Context()), "kind", kind, "error", err)
		b = []byte("null")
	}
	err = s.d.Audit.InsertAudit(context.WithoutCancel(r.Context()), store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: botID, Kind: kind, Actor: "panel:" + sess.User, Target: target,
		Detail: string(b), CreatedAt: s.now().UTC(),
	})
	if err != nil {
		s.log.Warn("panel change made but audit failed", "trace_id", traceID(r.Context()), "kind", kind, "error", err)
	}
}
