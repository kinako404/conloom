package panel

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const cookieName = "conloom_panel"

// touchInterval is how stale a session's last_seen_at may get before a
// request updates it.
const touchInterval = time.Minute

type sessionResponse struct {
	User      string    `json:"user"`
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type loginRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// login checks a name and password and starts a session. A wrong password and
// an unknown name get the same response after the same work; failures, and
// attempts still being checked, are rate-limited per source IP and in total.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		s.writeError(w, r, http.StatusForbidden, conloomapi.CodeForbidden, "cross-origin request")
		return
	}
	ip := remoteIP(r)
	now := s.now()
	if d := s.limiter.begin(ip, now); d > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(int64((d+time.Second-1)/time.Second), 10))
		s.writeError(w, r, http.StatusTooManyRequests, conloomapi.CodeRateLimited, "too many failed logins; try again later")
		return
	}
	failed := false
	defer func() { s.limiter.end(ip, s.now(), failed) }()
	var req loginRequest
	if !s.decode(w, r, &req) {
		return
	}
	user, known := s.user(req.Name)
	hash := s.dummyHash
	if known {
		hash = user.PasswordHash
	}
	ok, err := VerifyPassword(hash, req.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok || !known {
		failed = true
		if s.d.Metrics != nil {
			s.d.Metrics.PanelLoginFailures.Inc()
		}
		attrs := []any{"trace_id", traceID(r.Context()), "ip", ip}
		if known {
			attrs = append(attrs, "user", user.Name)
		}
		s.log.Warn("panel login failed", attrs...)
		s.writeError(w, r, http.StatusUnauthorized, conloomapi.CodeUnauthorized, "wrong name or password")
		return
	}

	token, err := randomToken()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	csrf, err := randomToken()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ttl := s.d.Config.Panel.SessionTTL.Duration
	ps := store.PanelSession{
		TokenHash: hashToken(token), User: user.Name, CSRFToken: csrf,
		CredFingerprint: CredFingerprint(user.PasswordHash),
		CreatedAt:       now.UTC(), LastSeenAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC(),
	}
	if err := s.d.Sessions.CreatePanelSession(r.Context(), ps); err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, s.cookie(token, int(ttl/time.Second)))
	s.audit(r, &Session{User: user.Name}, "", "panel.login", "", map[string]string{"ip": ip})
	s.writeJSON(w, http.StatusOK, sessionResponse{User: ps.User, CSRFToken: ps.CSRFToken, ExpiresAt: ps.ExpiresAt})
}

func (s *Server) session(w http.ResponseWriter, _ *http.Request, sess *Session) {
	s.writeJSON(w, http.StatusOK, sessionResponse{User: sess.User, CSRFToken: sess.CSRFToken, ExpiresAt: sess.ExpiresAt.UTC()})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess *Session) {
	if err := s.d.Sessions.DeletePanelSession(r.Context(), sess.tokenHash); err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, s.cookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

// authenticate returns the request's session, writing a 401 response itself
// when there is none: no cookie, an unknown token, a session past its TTL or
// idle timeout or deleted meanwhile, or a user whose password hash changed or
// who was removed.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		s.unauthorized(w, r)
		return nil, false
	}
	tokenHash := hashToken(c.Value)
	ps, err := s.d.Sessions.GetPanelSession(r.Context(), tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		s.unauthorized(w, r)
		return nil, false
	}
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	now := s.now()
	user, known := s.user(ps.User)
	if !now.Before(ps.ExpiresAt) || !now.Before(ps.LastSeenAt.Add(s.d.Config.Panel.IdleTimeout.Duration)) ||
		!known || CredFingerprint(user.PasswordHash) != ps.CredFingerprint {
		s.unauthorized(w, r)
		return nil, false
	}
	if now.Sub(ps.LastSeenAt) > touchInterval {
		err := s.d.Sessions.TouchPanelSession(r.Context(), tokenHash, now.UTC())
		if errors.Is(err, store.ErrNotFound) {
			s.unauthorized(w, r)
			return nil, false
		}
		if err != nil {
			s.log.Warn("panel session not touched", "trace_id", traceID(r.Context()), "error", err)
		}
	}
	return &Session{User: ps.User, CSRFToken: ps.CSRFToken, ExpiresAt: ps.ExpiresAt, tokenHash: tokenHash}, true
}

func (s *Server) unauthorized(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusUnauthorized, conloomapi.CodeUnauthorized, "not logged in or the session has expired")
}

// checkCSRF writes a 403 response itself unless the request is same-origin
// and its X-CSRF-Token header is the session's CSRF token.
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request, sess *Session) bool {
	if !sameOrigin(r) {
		s.writeError(w, r, http.StatusForbidden, conloomapi.CodeForbidden, "cross-origin request")
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRFToken)) != 1 {
		s.writeError(w, r, http.StatusForbidden, conloomapi.CodeForbidden, "missing or wrong X-CSRF-Token header")
		return false
	}
	return true
}

// sameOrigin reports whether the request has no Origin header or one whose
// host matches the Host header.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// user returns the configured panel user called name.
func (s *Server) user(name string) (config.PanelUser, bool) {
	for _, u := range s.d.Config.Panel.Users {
		if u.Name == name {
			return u, true
		}
	}
	return config.PanelUser{}, false
}

// cookie is the session cookie holding value; a negative maxAge deletes it.
func (s *Server) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: cookieName, Value: value, Path: "/panel/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.d.Config.Panel.CookieSecure, SameSite: http.SameSiteStrictMode,
	}
}

// remoteIP is the host of RemoteAddr; forwarding headers are not trusted.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// randomToken is 32 random bytes in unpadded base64url.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is the hex SHA-256 of a session token, the form the store keeps.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
