package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	testUser     = "admin"
	testPassword = "fake-password-1"
	testIP       = "192.0.2.10"
	testHost     = "example.invalid"
)

var (
	testHashOnce sync.Once
	testHashes   [2]string
)

// testHash returns a hash of testPassword at MinIterations, computed once.
func testHash(t *testing.T) string {
	t.Helper()
	testHashOnce.Do(func() {
		for i := range testHashes {
			h, err := HashPassword(testPassword, MinIterations)
			if err != nil {
				panic(err)
			}
			testHashes[i] = h
		}
	})
	return testHashes[0]
}

// otherTestHash is another hash of testPassword, with a different salt.
func otherTestHash(t *testing.T) string {
	testHash(t)
	return testHashes[1]
}

func dur(d time.Duration) conloomapi.Duration { return conloomapi.Duration{Duration: d} }

type fakeSessions struct {
	mu       sync.Mutex
	m        map[string]store.PanelSession
	touches  int
	touchErr error
}

func (f *fakeSessions) CreatePanelSession(_ context.Context, s store.PanelSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[s.TokenHash] = s
	return nil
}

func (f *fakeSessions) GetPanelSession(_ context.Context, tokenHash string) (store.PanelSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.m[tokenHash]
	if !ok {
		return store.PanelSession{}, store.ErrNotFound
	}
	return s, nil
}

func (f *fakeSessions) TouchPanelSession(_ context.Context, tokenHash string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touchErr != nil {
		return f.touchErr
	}
	s, ok := f.m[tokenHash]
	if !ok {
		return store.ErrNotFound
	}
	s.LastSeenAt = at
	f.m[tokenHash] = s
	f.touches++
	return nil
}

func (f *fakeSessions) DeletePanelSession(_ context.Context, tokenHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, tokenHash)
	return nil
}

func (f *fakeSessions) all() []store.PanelSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.PanelSession
	for _, s := range f.m {
		out = append(out, s)
	}
	return out
}

type fakeAudit struct {
	mu     sync.Mutex
	events []store.AuditEvent
}

func (f *fakeAudit) InsertAudit(_ context.Context, a store.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, a)
	return nil
}

func (f *fakeAudit) all() []store.AuditEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.AuditEvent(nil), f.events...)
}

type harness struct {
	t        *testing.T
	srv      *Server
	h        http.Handler
	cfg      *config.Config
	sessions *fakeSessions
	audit    *fakeAudit
	reg      *prometheus.Registry
	now      time.Time
}

func newHarness(t *testing.T, mutate func(*config.PanelConfig)) *harness {
	t.Helper()
	pc := &config.PanelConfig{
		Users:       []config.PanelUser{{Name: testUser, PasswordHash: testHash(t)}},
		SessionTTL:  dur(config.DefaultPanelSessionTTL),
		IdleTimeout: dur(config.DefaultPanelIdleTimeout),
	}
	if mutate != nil {
		mutate(pc)
	}
	h := &harness{
		t:        t,
		cfg:      &config.Config{Panel: pc},
		sessions: &fakeSessions{m: map[string]store.PanelSession{}},
		audit:    &fakeAudit{},
		reg:      prometheus.NewRegistry(),
		now:      time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC),
	}
	h.srv = New(Deps{
		Config:   h.cfg,
		Sessions: h.sessions,
		Audit:    h.audit,
		Assets:   fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>织语</title>")}},
		Metrics:  obs.NewMetrics(h.reg),
		Now:      func() time.Time { return h.now },
	})
	h.h = h.srv.Handler()
	return h
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func withCSRF(token string) reqOpt {
	return func(r *http.Request) { r.Header.Set("X-CSRF-Token", token) }
}
func withOrigin(origin string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Origin", origin) }
}
func withRemoteAddr(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }

func (h *harness) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, "http://"+testHost+path, strings.NewReader(body))
	r.RemoteAddr = testIP + ":40000"
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

func (h *harness) login(name, password string, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "password": password})
	return h.do(http.MethodPost, "/panel/api/login", string(body), opts...)
}

// loginOK logs in as testUser and returns the session cookie and CSRF token.
func (h *harness) loginOK() (*http.Cookie, string) {
	h.t.Helper()
	w := h.login(testUser, testPassword)
	if w.Code != http.StatusOK {
		h.t.Fatalf("login status %d, body %s", w.Code, w.Body)
	}
	var resp sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode login response: %v", err)
	}
	return sessionCookie(h.t, w), resp.CSRFToken
}

func (h *harness) counter(name string, labels map[string]string) float64 {
	h.t.Helper()
	mfs, err := h.reg.Gather()
	if err != nil {
		h.t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metrics:
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if v, ok := labels[lp.GetName()]; ok && v != lp.GetValue() {
					continue metrics
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", cookieName, w.Header()["Set-Cookie"])
	return nil
}

func errorBody(t *testing.T, w *httptest.ResponseRecorder, status int, code string) conloomapi.ErrorBody {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, status, w.Body)
	}
	var resp conloomapi.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body %s: %v", w.Body, err)
	}
	if resp.Error.Code != code {
		t.Errorf("code = %q, want %q", resp.Error.Code, code)
	}
	if resp.Error.TraceID == "" {
		t.Error("error body has no trace_id")
	}
	return resp.Error
}

func TestLogin_SetsCookieAndReturnsCSRF(t *testing.T) {
	h := newHarness(t, nil)
	w := h.login(testUser, testPassword)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", w.Code, w.Body)
	}
	var resp struct {
		User      string     `json:"user"`
		CSRFToken string     `json:"csrf_token"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	wantExpiry := h.now.Add(config.DefaultPanelSessionTTL)
	if resp.User != testUser || len(resp.CSRFToken) < 43 || resp.ExpiresAt == nil || !resp.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("response = %+v, want user %q, a CSRF token and expiry %s", resp, testUser, wantExpiry)
	}

	c := sessionCookie(t, w)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/panel/" || c.Secure {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Strict, Path=/panel/, not Secure", c)
	}
	if len(c.Value) != 43 {
		t.Errorf("cookie value has %d chars, want 43 (32 bytes base64url)", len(c.Value))
	}

	stored := h.sessions.all()
	if len(stored) != 1 {
		t.Fatalf("stored sessions = %d, want 1", len(stored))
	}
	sum := sha256.Sum256([]byte(c.Value))
	s := stored[0]
	if s.TokenHash != hex.EncodeToString(sum[:]) {
		t.Errorf("TokenHash = %q, want the SHA-256 hex of the cookie value", s.TokenHash)
	}
	for _, v := range []string{s.TokenHash, s.User, s.CSRFToken, s.CredFingerprint} {
		if v == c.Value {
			t.Errorf("the session store holds the raw token")
		}
	}
	if s.User != testUser || s.CSRFToken != resp.CSRFToken || s.CredFingerprint != CredFingerprint(testHash(t)) ||
		!s.CreatedAt.Equal(h.now) || !s.LastSeenAt.Equal(h.now) || !s.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("stored session = %+v", s)
	}

	events := h.audit.all()
	if len(events) != 1 || events[0].Kind != "panel.login" || events[0].Actor != "panel:"+testUser || events[0].ID == "" {
		t.Errorf("audit = %+v, want one panel.login by panel:%s", events, testUser)
	}

	g := h.do(http.MethodGet, "/panel/api/session", "", withCookie(c))
	if g.Code != http.StatusOK {
		t.Fatalf("GET session status %d, body %s", g.Code, g.Body)
	}
	var again sessionResponse
	if err := json.Unmarshal(g.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.User != testUser || again.CSRFToken != resp.CSRFToken || !again.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("GET session = %+v", again)
	}

	secure := newHarness(t, func(pc *config.PanelConfig) { pc.CookieSecure = true })
	if c := sessionCookie(t, secure.login(testUser, testPassword)); !c.Secure {
		t.Errorf("cookie_secure: cookie = %+v, want Secure", c)
	}
}

func TestLogin_WrongPasswordAndUnknownUser(t *testing.T) {
	h := newHarness(t, nil)
	wrong := errorBody(t, h.login(testUser, "not-the-password"), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	unknown := errorBody(t, h.login("nobody", testPassword), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	wrong.TraceID, unknown.TraceID = "", ""
	if wrong != unknown {
		t.Errorf("wrong password body %+v differs from unknown user body %+v", wrong, unknown)
	}
	if got := h.counter("conloom_panel_login_failures_total", nil); got != 2 {
		t.Errorf("login failures = %v, want 2", got)
	}
	if n := len(h.sessions.all()); n != 0 {
		t.Errorf("sessions = %d, want none", n)
	}
	if n := len(h.audit.all()); n != 0 {
		t.Errorf("failed logins wrote %d audit events, want none", n)
	}

	errorBody(t, h.do(http.MethodPost, "/panel/api/login", `{"name":"admin","password":"x","extra":1}`),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.do(http.MethodPost, "/panel/api/login", `not json`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
}

func TestLogin_RateLimited(t *testing.T) {
	t.Parallel() // over 30 PBKDF2 runs; slow under -race
	h := newHarness(t, nil)
	ip := func(n int) reqOpt { return withRemoteAddr(fmt.Sprintf("192.0.2.%d:5000", n)) }

	for i := range 5 {
		if w := h.login(testUser, "wrong", ip(1)); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d", i+1, w.Code)
		}
		h.advance(time.Minute)
	}
	w := h.login(testUser, testPassword, ip(1))
	errorBody(t, w, http.StatusTooManyRequests, conloomapi.CodeRateLimited)
	// The first failure was 5 minutes ago, so it leaves the window in 10.
	if got := w.Header().Get("Retry-After"); got != "600" {
		t.Errorf("Retry-After = %q, want 600", got)
	}
	if w := h.login(testUser, testPassword, ip(2)); w.Code != http.StatusOK {
		t.Fatalf("another IP: status %d, want 200", w.Code)
	}

	for n := 2; n <= 6; n++ {
		for range 5 {
			if w := h.login(testUser, "wrong", ip(n)); w.Code != http.StatusUnauthorized {
				t.Fatalf("ip %d: status %d, want 401", n, w.Code)
			}
		}
	}
	w = h.login(testUser, testPassword, ip(7))
	errorBody(t, w, http.StatusTooManyRequests, conloomapi.CodeRateLimited)
	if w.Header().Get("Retry-After") == "" {
		t.Error("global limit: no Retry-After")
	}
	if got := h.counter("conloom_panel_login_failures_total", nil); got != 30 {
		t.Errorf("login failures = %v, want 30 (rejected attempts are not failures)", got)
	}

	h.advance(15 * time.Minute)
	for _, n := range []int{1, 7} {
		if w := h.login(testUser, testPassword, ip(n)); w.Code != http.StatusOK {
			t.Errorf("ip %d after 15 minutes: status %d, want 200", n, w.Code)
		}
	}
}

func TestLogin_ConcurrentAttemptsAreLimited(t *testing.T) {
	t.Parallel() // over 30 PBKDF2 runs; slow under -race
	h := newHarness(t, nil)
	burst := func(n int, addr func(i int) string) map[int]int {
		codes := make([]int, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { codes[i] = h.login(testUser, "wrong", withRemoteAddr(addr(i))).Code })
		}
		wg.Wait()
		count := map[int]int{}
		for _, c := range codes {
			count[c]++
		}
		return count
	}

	got := burst(12, func(int) string { return "192.0.2.1:5000" })
	if got[http.StatusUnauthorized] != 5 || got[http.StatusTooManyRequests] != 7 {
		t.Errorf("12 concurrent attempts from one IP: %v, want 5×401 and 7×429", got)
	}

	h.advance(15 * time.Minute)
	got = burst(40, func(i int) string { return fmt.Sprintf("198.51.100.%d:5000", i+1) })
	if got[http.StatusUnauthorized] != 30 || got[http.StatusTooManyRequests] != 10 {
		t.Errorf("40 concurrent attempts from 40 IPs: %v, want 30×401 and 10×429", got)
	}
}

func TestLogin_RejectedBodiesAreNotFailures(t *testing.T) {
	h := newHarness(t, nil)
	for range 8 {
		errorBody(t, h.do(http.MethodPost, "/panel/api/login", `not json`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
	if w := h.login(testUser, testPassword); w.Code != http.StatusOK {
		t.Errorf("login after bad bodies: %d, want 200", w.Code)
	}
}

func TestAPI_RequiresSession(t *testing.T) {
	get := func(h *harness, opts ...reqOpt) int {
		return h.do(http.MethodGet, "/panel/api/session", "", opts...).Code
	}
	t.Run("no or bad cookie", func(t *testing.T) {
		h := newHarness(t, nil)
		errorBody(t, h.do(http.MethodGet, "/panel/api/session", ""), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
		if code := get(h, withCookie(&http.Cookie{Name: cookieName, Value: "forged"})); code != http.StatusUnauthorized {
			t.Errorf("forged cookie: %d, want 401", code)
		}
	})
	t.Run("session ttl", func(t *testing.T) {
		h := newHarness(t, func(pc *config.PanelConfig) {
			pc.SessionTTL = dur(2 * time.Hour)
			pc.IdleTimeout = dur(time.Hour)
		})
		c, _ := h.loginOK()
		for range 2 {
			h.advance(50 * time.Minute)
			if code := get(h, withCookie(c)); code != http.StatusOK {
				t.Fatalf("active session: %d, want 200", code)
			}
		}
		h.advance(20 * time.Minute)
		if code := get(h, withCookie(c)); code != http.StatusUnauthorized {
			t.Errorf("expired session: %d, want 401", code)
		}
	})
	t.Run("idle timeout", func(t *testing.T) {
		h := newHarness(t, func(pc *config.PanelConfig) { pc.IdleTimeout = dur(time.Hour) })
		c, _ := h.loginOK()
		h.advance(30 * time.Second)
		get(h, withCookie(c))
		if h.sessions.touches != 0 {
			t.Errorf("touched %d times within a minute, want 0", h.sessions.touches)
		}
		h.advance(59 * time.Minute)
		if code := get(h, withCookie(c)); code != http.StatusOK {
			t.Fatalf("before idle timeout: %d, want 200", code)
		}
		if h.sessions.touches != 1 {
			t.Errorf("touches = %d, want 1", h.sessions.touches)
		}
		h.advance(time.Hour)
		if code := get(h, withCookie(c)); code != http.StatusUnauthorized {
			t.Errorf("idle session: %d, want 401", code)
		}
	})
	t.Run("password changed", func(t *testing.T) {
		h := newHarness(t, nil)
		c, _ := h.loginOK()
		h.cfg.Panel.Users[0].PasswordHash = otherTestHash(t)
		if code := get(h, withCookie(c)); code != http.StatusUnauthorized {
			t.Errorf("after password change: %d, want 401", code)
		}
	})
	t.Run("session gone before touch", func(t *testing.T) {
		h := newHarness(t, nil)
		c, _ := h.loginOK()
		h.advance(2 * time.Minute)
		h.sessions.touchErr = fmt.Errorf("touch: %w", store.ErrNotFound)
		errorBody(t, h.do(http.MethodGet, "/panel/api/session", "", withCookie(c)), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	})
	t.Run("touch failure is not fatal", func(t *testing.T) {
		h := newHarness(t, nil)
		c, _ := h.loginOK()
		h.advance(2 * time.Minute)
		h.sessions.touchErr = errors.New("database is locked")
		if code := get(h, withCookie(c)); code != http.StatusOK {
			t.Errorf("after a failed touch: %d, want 200", code)
		}
	})
	t.Run("user removed", func(t *testing.T) {
		h := newHarness(t, nil)
		c, _ := h.loginOK()
		h.cfg.Panel.Users[0].Name = "someone-else"
		if code := get(h, withCookie(c)); code != http.StatusUnauthorized {
			t.Errorf("after user removal: %d, want 401", code)
		}
	})
}

func TestAPI_CSRF(t *testing.T) {
	h := newHarness(t, nil)
	var calls int
	h.srv.api("PUT /panel/api/test/{x}", func(w http.ResponseWriter, r *http.Request, sess *Session) {
		calls++
		h.srv.writeJSON(w, http.StatusOK, map[string]string{"user": sess.User, "x": r.PathValue("x")})
	})
	h.srv.api("GET /panel/api/test/{x}", func(w http.ResponseWriter, r *http.Request, sess *Session) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	})
	c, csrf := h.loginOK()

	errorBody(t, h.do(http.MethodPut, "/panel/api/test/a", "{}", withCookie(c)), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.do(http.MethodPut, "/panel/api/test/a", "{}", withCookie(c), withCSRF(csrf+"x")),
		http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.do(http.MethodPut, "/panel/api/test/a", "{}", withCookie(c), withCSRF(csrf),
		withOrigin("http://evil.example.invalid")), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.do(http.MethodPut, "/panel/api/test/a", "{}", withCookie(c), withCSRF(csrf), withOrigin("null")),
		http.StatusForbidden, conloomapi.CodeForbidden)
	if calls != 0 {
		t.Fatalf("handler ran %d times for rejected requests", calls)
	}
	errorBody(t, h.do(http.MethodPut, "/panel/api/test/a", "{}", withCSRF(csrf)), http.StatusUnauthorized, conloomapi.CodeUnauthorized)

	if w := h.do(http.MethodPut, "/panel/api/test/a", "{}", withCookie(c), withCSRF(csrf)); w.Code != http.StatusOK {
		t.Errorf("valid PUT: %d %s", w.Code, w.Body)
	}
	if w := h.do(http.MethodPut, "/panel/api/test/a", "{}", withCookie(c), withCSRF(csrf), withOrigin("http://"+testHost)); w.Code != http.StatusOK {
		t.Errorf("same-origin PUT: %d %s", w.Code, w.Body)
	}
	if w := h.do(http.MethodGet, "/panel/api/test/a", "", withCookie(c)); w.Code != http.StatusNoContent {
		t.Errorf("GET without CSRF header: %d, want 204", w.Code)
	}

	errorBody(t, h.login(testUser, testPassword, withOrigin("https://evil.example.invalid")),
		http.StatusForbidden, conloomapi.CodeForbidden)
}

func TestLogout_DeletesSession(t *testing.T) {
	h := newHarness(t, nil)
	c, csrf := h.loginOK()
	errorBody(t, h.do(http.MethodPost, "/panel/api/logout", "", withCookie(c)), http.StatusForbidden, conloomapi.CodeForbidden)

	w := h.do(http.MethodPost, "/panel/api/logout", "", withCookie(c), withCSRF(csrf))
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout status %d, body %s", w.Code, w.Body)
	}
	if cleared := sessionCookie(t, w); cleared.MaxAge >= 0 || cleared.Value != "" || cleared.Path != "/panel/" {
		t.Errorf("logout cookie = %+v, want it cleared", cleared)
	}
	if n := len(h.sessions.all()); n != 0 {
		t.Errorf("sessions after logout = %d, want 0", n)
	}
	errorBody(t, h.do(http.MethodGet, "/panel/api/session", "", withCookie(c)), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
}

const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t, nil)
	c, _ := h.loginOK()
	for _, tc := range []struct {
		path string
		opts []reqOpt
		api  bool
	}{
		{"/panel/", nil, false},
		{"/panel/missing.js", nil, false},
		{"/panel/api/session", nil, true},
		{"/panel/api/session", []reqOpt{withCookie(c)}, true},
		{"/panel/api/no-such-route", []reqOpt{withCookie(c)}, true},
	} {
		w := h.do(http.MethodGet, tc.path, "", tc.opts...)
		hd := w.Header()
		if got := hd.Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("%s: CSP = %q", tc.path, got)
		}
		if got := hd.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", tc.path, got)
		}
		if got := hd.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q", tc.path, got)
		}
		if got := hd.Get("Cache-Control"); tc.api && got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", tc.path, got)
		}
	}
	errorBody(t, h.do(http.MethodGet, "/panel/api/no-such-route", "", withCookie(c)), http.StatusNotFound, conloomapi.CodeNotFound)
}

func TestMetrics_RequestsByRouteAndCode(t *testing.T) {
	h := newHarness(t, nil)
	h.do(http.MethodGet, "/panel/api/session", "")
	h.do(http.MethodGet, "/panel/api/session", "")
	h.loginOK()
	if got := h.counter("conloom_panel_requests_total", map[string]string{"route": "GET /panel/api/session", "code": "401"}); got != 2 {
		t.Errorf("GET session 401 = %v, want 2", got)
	}
	if got := h.counter("conloom_panel_requests_total", map[string]string{"route": "POST /panel/api/login", "code": "200"}); got != 1 {
		t.Errorf("POST login 200 = %v, want 1", got)
	}
}

func TestHelpers_DecodeFailAndAudit(t *testing.T) {
	h := newHarness(t, nil)
	h.srv.api("POST /panel/api/test/decode", func(w http.ResponseWriter, r *http.Request, sess *Session) {
		var v struct {
			A int `json:"a"`
		}
		if !h.srv.decode(w, r, &v) {
			return
		}
		h.srv.audit(r, sess, "qq-main", "policy.update", "qq-main:qq:group:10001", map[string]int{"a": v.A})
		h.srv.writeJSON(w, http.StatusOK, v)
	})
	h.srv.api("GET /panel/api/test/fail/{kind}", func(w http.ResponseWriter, r *http.Request, sess *Session) {
		switch r.PathValue("kind") {
		case "notfound":
			h.srv.fail(w, r, fmt.Errorf("lookup: %w", store.ErrNotFound))
		default:
			h.srv.fail(w, r, errors.New("disk on fire at /var/secret"))
		}
	})
	c, csrf := h.loginOK()
	post := func(body string) *httptest.ResponseRecorder {
		return h.do(http.MethodPost, "/panel/api/test/decode", body, withCookie(c), withCSRF(csrf))
	}

	if w := post(`{"a":7}`); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"a":7}` {
		t.Errorf("decode ok: %d %s", w.Code, w.Body)
	}
	errorBody(t, post(`{"a":7,"b":1}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, post(`{"a":`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, post(`{"a":1} {"a":2}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, post(`{"a":1,"pad":"`+strings.Repeat("x", 1<<20)+`"}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)

	events := h.audit.all()
	last := events[len(events)-1]
	if len(events) != 2 || last.Kind != "policy.update" || last.Actor != "panel:"+testUser || last.BotInstanceID != "qq-main" ||
		last.Target != "qq-main:qq:group:10001" || last.Detail != `{"a":7}` || !last.CreatedAt.Equal(h.now) || last.ID == "" {
		t.Errorf("audit events = %+v", events)
	}

	errorBody(t, h.do(http.MethodGet, "/panel/api/test/fail/notfound", "", withCookie(c)), http.StatusNotFound, conloomapi.CodeNotFound)
	w := h.do(http.MethodGet, "/panel/api/test/fail/other", "", withCookie(c))
	errorBody(t, w, http.StatusInternalServerError, conloomapi.CodeInternal)
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("500 body leaks the error: %s", w.Body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/panel/api/x", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.srv.fail(rec, r, fmt.Errorf("query: %w", context.Canceled))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("cancelled request: %d, want 503", rec.Code)
	}
}
