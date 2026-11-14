// Package panel serves the web admin panel: the single-page app under /panel/
// and its JSON API under /panel/api/, authenticated by a session cookie.
package panel

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type SessionStore interface {
	CreatePanelSession(ctx context.Context, s store.PanelSession) error
	GetPanelSession(ctx context.Context, tokenHash string) (store.PanelSession, error)
	TouchPanelSession(ctx context.Context, tokenHash string, at time.Time) error
	DeletePanelSession(ctx context.Context, tokenHash string) error
}

type Auditor interface {
	InsertAudit(ctx context.Context, a store.AuditEvent) error
}

// Deps are the panel's collaborators. Config.Panel must be non-nil. Assets is
// the web root, with index.html at the top. A nil Metrics records nothing, a
// nil Logger discards and a nil Now is time.Now.
//
// The fields after Now serve the API beyond logging in. Memory starts a
// memory consolidation; Gatherer is the registry the live metrics are read
// from; Presence holds when each bot's adapter last called the core; a nil
// Location is UTC; StartedAt is when the process started.
type Deps struct {
	Config   *config.Config
	Sessions SessionStore
	Audit    Auditor
	Assets   fs.FS
	Metrics  *obs.Metrics
	Logger   *slog.Logger
	Now      func() time.Time

	Store           Store
	Policies        *policy.Service
	Personas        *persona.Service
	MemoryAdmin     *memory.Admin
	Memory          interface{ Trigger(convKey string) }
	Gatherer        prometheus.Gatherer
	Presence        *obs.Presence
	Location        *time.Location
	Version, Commit string
	StartedAt       time.Time
}

type Server struct {
	d       Deps
	log     *slog.Logger
	now     func() time.Time
	mux     *http.ServeMux
	limiter *loginLimiter
	// dummyHash is verified for unknown login names, so they cost as much
	// as known ones.
	dummyHash string
}

// Session is the logged-in session of an API request.
type Session struct {
	User, CSRFToken string
	ExpiresAt       time.Time

	tokenHash string
}

// apiHandler serves an authenticated API request.
type apiHandler func(w http.ResponseWriter, r *http.Request, sess *Session)

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func New(d Deps) *Server {
	s := &Server{
		d: d, log: d.Logger, now: d.Now, mux: http.NewServeMux(), limiter: newLoginLimiter(),
		dummyHash: dummyHash(d.Config.Panel.Users),
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.routes()
	return s
}

// dummyHash is a well-formed hash that matches no password, with the highest
// iteration count among users.
func dummyHash(users []config.PanelUser) string {
	iterations := 0
	for _, u := range users {
		if n, err := ParseHash(u.PasswordHash); err == nil {
			iterations = max(iterations, n)
		}
	}
	if iterations == 0 {
		iterations = DefaultIterations
	}
	return fmt.Sprintf("%s:%d:%s:%s", hashScheme, iterations,
		b64.EncodeToString(make([]byte, saltLen)), b64.EncodeToString(make([]byte, keyLen)))
}

// Handler serves /panel and everything under /panel/, and redirects / to
// /panel/ for a listener of its own; every other path is not found. Every
// response carries the security headers and API responses are not cached;
// every request gets a trace ID and is counted by route pattern and status;
// panics become 500 INTERNAL_ERROR responses.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/panel/api/") {
			h.Set("Cache-Control", "no-store")
		}
		r = r.WithContext(context.WithValue(r.Context(), traceKey{}, ids.New("trace")))
		_, pattern := s.mux.Handler(r)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic serving panel request", "trace_id", traceID(r.Context()), "method", r.Method,
					"path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				s.writeError(rec, r, http.StatusInternalServerError, conloomapi.CodeInternal, "internal error")
			}
			s.countRequest(pattern, rec.status)
		}()
		s.mux.ServeHTTP(rec, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /panel/api/login", s.login)
	s.api("GET /panel/api/session", s.session)
	s.api("POST /panel/api/logout", s.logout)
	s.overviewRoutes()
	s.dataRoutes()
	s.writeRoutes()
	s.mux.HandleFunc("/panel/api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, "no such API")
	})
	s.mux.HandleFunc("GET /panel", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/panel/", http.StatusMovedPermanently)
	})
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/panel/", http.StatusFound)
	})
	s.mux.HandleFunc("/panel/", s.static)
}

// api registers h for pattern behind session authentication; requests with
// methods other than GET and HEAD must also pass the CSRF checks.
func (s *Server) api(pattern string, h apiHandler) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.checkCSRF(w, r, sess) {
			return
		}
		h(w, r, sess)
	})
}

func (s *Server) countRequest(pattern string, status int) {
	if s.d.Metrics == nil {
		return
	}
	if pattern == "" {
		pattern = "unmatched"
	}
	s.d.Metrics.PanelRequests.WithLabelValues(pattern, strconv.Itoa(status)).Inc()
}

// statusRecorder remembers the response status.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wrote && code >= http.StatusOK {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type traceKey struct{}

func traceID(ctx context.Context) string {
	id, _ := ctx.Value(traceKey{}).(string)
	return id
}
