package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	maxBodyBytes       = 1 << 20
	defaultOutboxLimit = 10
	maxOutboxLimit     = 50
	healthTimeout      = 2 * time.Second
)

var allTriggerTypes = []string{
	conloomapi.TriggerPrivate, conloomapi.TriggerMention, conloomapi.TriggerReply,
	conloomapi.TriggerCommand, conloomapi.TriggerObserve, conloomapi.TriggerManual,
}

func (s *Server) invalid(w http.ResponseWriter, r *http.Request, message string) {
	s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, message, false, nil)
}

// readBody reads at most maxBodyBytes of the request body, reporting a 400
// response itself on failure.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
		s.invalid(w, r, "request body exceeds 1 MiB")
		return nil, false
	}
	if err != nil {
		s.invalid(w, r, "cannot read request body")
		return nil, false
	}
	return body, true
}

// decodeBody decodes the JSON request body into v, reporting a 400 response
// itself on failure.
func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, ok := s.readBody(w, r)
	if !ok {
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		s.invalid(w, r, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// bot returns the token's bot, reporting a 404 response itself when it is not configured.
func (s *Server) bot(w http.ResponseWriter, r *http.Request, tok *token) (config.BotConfig, bool) {
	bot, ok := s.d.Config.Bot(tok.botID)
	if !ok {
		s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, "bot not configured", false, nil)
	}
	return bot, ok
}

func (s *Server) postEvent(w http.ResponseWriter, r *http.Request, tok *token) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		s.invalid(w, r, "Idempotency-Key header is required")
		return
	}
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var ev conloomapi.Event
	if err := json.Unmarshal(body, &ev); err != nil {
		s.invalid(w, r, "invalid JSON body: "+err.Error())
		return
	}
	if ev.BotInstanceID != tok.botID {
		s.writeError(w, r, http.StatusForbidden, conloomapi.CodeForbidden,
			"token is not bound to bot_instance_id "+strconv.Quote(ev.BotInstanceID), false, nil)
		return
	}
	bot, ok := s.bot(w, r, tok)
	if !ok {
		return
	}
	if err := validateEvent(ev, bot); err != nil {
		s.invalid(w, r, err.Error())
		return
	}
	resp, replayed, err := s.d.Orchestrator.Submit(r.Context(), tok.botID, key, body, ev)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request, tok *token) {
	task, err := s.d.Orchestrator.GetTask(r.Context(), tok.botID, r.PathValue("task_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request, tok *token) {
	task, err := s.d.Orchestrator.CancelTask(r.Context(), tok.botID, r.PathValue("task_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) pollOutbox(w http.ResponseWriter, r *http.Request, tok *token) {
	q := r.URL.Query()
	limit := defaultOutboxLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			s.invalid(w, r, "limit must be an integer")
			return
		}
		limit = min(max(n, 1), maxOutboxLimit)
	}
	var wait time.Duration
	if v := q.Get("wait"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			s.invalid(w, r, "wait must be a non-negative duration such as 30s")
			return
		}
		wait = d
	}
	actions, err := s.d.Outbox.Poll(r.Context(), tok.botID, limit, wait)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if actions == nil {
		actions = []conloomapi.Action{}
	}
	writeJSON(w, http.StatusOK, conloomapi.OutboxResponse{Actions: actions})
}

func (s *Server) ackOutbox(w http.ResponseWriter, r *http.Request, tok *token) {
	var req conloomapi.AckRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	switch req.Status {
	case conloomapi.AckSent, conloomapi.AckUnsupported, conloomapi.AckFailed:
	default:
		s.invalid(w, r, "status must be one of sent|unsupported|failed, got "+strconv.Quote(req.Status))
		return
	}
	if err := s.d.Outbox.Ack(r.Context(), tok.botID, r.PathValue("action_id"), req); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request, tok *token) {
	bot, ok := s.bot(w, r, tok)
	if !ok {
		return
	}
	enabled := bot.EnabledConversations
	if enabled == nil {
		enabled = []conloomapi.ConversationRef{}
	}
	writeJSON(w, http.StatusOK, conloomapi.Capabilities{
		SchemaVersion:        conloomapi.SchemaVersion,
		BotInstanceID:        bot.ID,
		BotName:              bot.Name,
		PlatformSelfID:       bot.PlatformSelfID,
		Scopes:               append([]string{}, tok.scopes...),
		EventTypes:           []string{conloomapi.EventTypeMessageCreated},
		ActionTypes:          []string{conloomapi.ActionMessageSend},
		TriggerTypes:         allTriggerTypes,
		EnabledConversations: enabled,
		PrivateChat:          bot.PrivateChat,
	})
}

// conversation resolves the {id} path value within the token's bot,
// reporting an error response itself on failure.
func (s *Server) conversation(w http.ResponseWriter, r *http.Request, tok *token) (store.ConversationRecord, bool) {
	conv, err := s.d.Store.GetConversation(r.Context(), tok.botID, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return store.ConversationRecord{}, false
	}
	return conv, true
}

func personaState(convID string, pv store.PersonaVersion) conloomapi.PersonaState {
	return conloomapi.PersonaState{
		ConversationID: convID, Version: pv.Version, Persona: pv.Persona,
		Reason: pv.Reason, UpdatedBy: pv.UpdatedBy, CreatedAt: pv.CreatedAt,
	}
}

func policyState(convID string, pv store.PolicyVersion) conloomapi.PolicyState {
	return conloomapi.PolicyState{
		ConversationID: convID, Version: pv.Version, Policy: pv.Policy,
		Reason: pv.Reason, UpdatedBy: pv.UpdatedBy, CreatedAt: pv.CreatedAt,
	}
}

func (s *Server) getPersona(w http.ResponseWriter, r *http.Request, tok *token) {
	bot, ok := s.bot(w, r, tok)
	if !ok {
		return
	}
	conv, ok := s.conversation(w, r, tok)
	if !ok {
		return
	}
	pv, err := s.d.Personas.Current(r.Context(), conv.Key, bot.PersonaTemplate)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, personaState(conv.ID, pv))
}

func (s *Server) putPersona(w http.ResponseWriter, r *http.Request, tok *token) {
	bot, ok := s.bot(w, r, tok)
	if !ok {
		return
	}
	conv, ok := s.conversation(w, r, tok)
	if !ok {
		return
	}
	var put conloomapi.PersonaPut
	if !s.decodeBody(w, r, &put) {
		return
	}
	pv, err := s.d.Personas.Update(r.Context(), conv.Key, bot.PersonaTemplate, put, tok.name)
	if !s.stored(w, r, pv.Version, err) {
		return
	}
	writeJSON(w, http.StatusOK, personaState(conv.ID, pv))
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.conversation(w, r, tok)
	if !ok {
		return
	}
	pv, err := s.d.Policies.Current(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, policyState(conv.ID, pv))
}

func (s *Server) putPolicy(w http.ResponseWriter, r *http.Request, tok *token) {
	conv, ok := s.conversation(w, r, tok)
	if !ok {
		return
	}
	var put conloomapi.PolicyPut
	if !s.decodeBody(w, r, &put) {
		return
	}
	pv, err := s.d.Policies.Update(r.Context(), conv.Key, put, tok.name)
	if !s.stored(w, r, pv.Version, err) {
		return
	}
	writeJSON(w, http.StatusOK, policyState(conv.ID, pv))
}

// stored reports whether an update produced a version. An error returned
// together with a stored version (a failed audit write) is only logged.
func (s *Server) stored(w http.ResponseWriter, r *http.Request, version int, err error) bool {
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

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	if _, err := s.d.Store.GetEvent(ctx, "", ""); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "database unavailable", true, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}
