// Package gateway serves the ConLoom Bot Interaction API over HTTP.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/outbox"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// Deps are the gateway's collaborators. Metrics is served at /metrics when
// Config.Server.MetricsEnabled is set; a nil Logger discards. Memory starts
// consolidations asked for through the API; when nil they are accepted but
// nothing runs. Counters records affinity changes made through the API; nil
// records nothing. MemoryAdmin serves the memory endpoints; when nil one is
// built from Store, Policies and Counters. Presence records each bot whose
// token authenticated a request; nil records nothing. Panel, when not nil, is
// served at /panel and under /panel/.
type Deps struct {
	Orchestrator *core.Orchestrator
	Outbox       *outbox.Service
	Personas     *persona.Service
	Policies     *policy.Service
	Store        store.Store
	Config       *config.Config
	Metrics      http.Handler
	Logger       *slog.Logger
	Memory       interface{ Trigger(convKey string) }
	Counters     *obs.Metrics
	MemoryAdmin  *memory.Admin
	Presence     *obs.Presence
	Panel        http.Handler
}

type Server struct {
	d      Deps
	log    *slog.Logger
	tokens []token
	mux    *http.ServeMux
}

func New(d Deps) *Server {
	if d.MemoryAdmin == nil {
		d.MemoryAdmin = memory.NewAdmin(memory.AdminDeps{Store: d.Store, Policies: d.Policies, Metrics: d.Counters})
	}
	s := &Server{d: d, log: d.Logger, tokens: newTokens(d.Config.APITokens), mux: http.NewServeMux()}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.routes()
	return s
}

// Handler returns the API handler. Every request gets a trace ID, and panics
// become 500 INTERNAL_ERROR responses.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), traceKey{}, ids.New("trace")))
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic serving request", "trace_id", traceID(r.Context()), "method", r.Method,
					"path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				writeErrorBody(w, r, http.StatusInternalServerError, conloomapi.CodeInternal, "internal error", false)
			}
		}()
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.handle("POST /api/v1/events", conloomapi.ScopeEventsWrite, s.postEvent)
	s.handle("GET /api/v1/tasks/{task_id}", conloomapi.ScopeTasksRead, s.getTask)
	s.handle("POST /api/v1/tasks/{task_id}/cancel", conloomapi.ScopeTasksRead, s.cancelTask)
	s.handle("GET /api/v1/outbox", conloomapi.ScopeOutboxRead, s.pollOutbox)
	s.handle("POST /api/v1/outbox/{action_id}/ack", conloomapi.ScopeOutboxAck, s.ackOutbox)
	s.handle("GET /api/v1/capabilities", "", s.capabilities)
	s.handle("GET /api/v1/conversations/{id}/persona", conloomapi.ScopePersonaRead, s.getPersona)
	s.handle("PUT /api/v1/conversations/{id}/persona", conloomapi.ScopePolicyWrite, s.putPersona)
	s.handle("GET /api/v1/conversations/{id}/policy", conloomapi.ScopeAdminRead, s.getPolicy)
	s.handle("PUT /api/v1/conversations/{id}/policy", conloomapi.ScopePolicyWrite, s.putPolicy)
	s.handle("GET /api/v1/conversations/{id}/memory", conloomapi.ScopeMemoryRead, s.getMemory)
	s.handle("PUT /api/v1/conversations/{id}/memory/members/{user_id}", conloomapi.ScopeMemoryWrite, s.putMemberMemory)
	s.handle("DELETE /api/v1/conversations/{id}/memory/members/{user_id}", conloomapi.ScopeMemoryWrite, s.deleteMemberMemory)
	s.handle("POST /api/v1/conversations/{id}/memory/consolidate", conloomapi.ScopeMemoryWrite, s.consolidateMemory)
	s.mux.HandleFunc("GET /healthz", s.healthz)
	if s.d.Config.Server.MetricsEnabled && s.d.Metrics != nil {
		s.mux.Handle("GET /metrics", s.d.Metrics)
	}
	if s.d.Panel != nil {
		s.mux.Handle("/panel", s.d.Panel)
		s.mux.Handle("/panel/", s.d.Panel)
	}
}

type traceKey struct{}

func traceID(ctx context.Context) string {
	id, _ := ctx.Value(traceKey{}).(string)
	return id
}
