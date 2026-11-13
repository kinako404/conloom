package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// fail maps err from a collaborator to an error response. Unrecognised errors
// are logged and reported as 500 without their details. A request whose
// context was cancelled (client gone or server shutting down) gets a retryable
// 503, so a still-connected client never mistakes it for success.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if ctxErr := r.Context().Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		s.log.Debug("request cancelled", "trace_id", traceID(r.Context()), "method", r.Method, "path", r.URL.Path)
		writeErrorBody(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "request cancelled; server may be shutting down", true)
		return
	}
	switch {
	case errors.Is(err, core.ErrDuplicateEvent):
		s.writeError(w, r, http.StatusConflict, conloomapi.CodeDuplicateEvent,
			"idempotency key reused with a different request body", false, err)
	case errors.Is(err, store.ErrDuplicateMessage):
		s.writeError(w, r, http.StatusConflict, conloomapi.CodeDuplicateEvent,
			"platform message already submitted", false, err)
	case errors.Is(err, core.ErrSchedulerClosed):
		s.writeError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "server is shutting down", true, err)
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, "not found", false, err)
	case errors.Is(err, persona.ErrInvalid), errors.Is(err, policy.ErrInvalid):
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, err.Error(), false, err)
	case errors.Is(err, memory.ErrInvalid):
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			strings.TrimPrefix(err.Error(), memory.ErrInvalid.Error()+": "), false, err)
	case errors.Is(err, memory.ErrUpdateConflict):
		s.writeError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "member memory kept changing; try again", true, err)
	case errors.Is(err, memory.ErrMemoryDisabled):
		s.writeError(w, r, http.StatusConflict, conloomapi.CodeMemoryDisabled, memory.ErrMemoryDisabled.Error(), false, err)
	case errors.Is(err, memory.ErrPaused):
		s.writeError(w, r, http.StatusConflict, conloomapi.CodePaused, memory.ErrPaused.Error(), false, err)
	default:
		s.writeError(w, r, http.StatusInternalServerError, conloomapi.CodeInternal, "internal error", false, err)
	}
}

// writeError logs the failure with the request's trace ID (at error level for
// 5xx) and writes the error body. cause is logged, never sent.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, cause error) {
	level := slog.LevelDebug
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	attrs := []any{"trace_id", traceID(r.Context()), "method", r.Method, "path", r.URL.Path, "status", status, "code", code}
	if cause != nil {
		attrs = append(attrs, "error", cause)
	}
	s.log.Log(r.Context(), level, "request failed", attrs...)
	writeErrorBody(w, r, status, code, message, retryable)
}

func writeErrorBody(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool) {
	writeJSON(w, status, conloomapi.ErrorResponse{Error: conloomapi.ErrorBody{
		Code: code, Message: message, Retryable: retryable, TraceID: traceID(r.Context()),
	}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
