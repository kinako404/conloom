package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"slices"
	"strings"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// token is a configured API token; only its SHA-256 hash is kept.
type token struct {
	name   string
	botID  string
	scopes []string
	hash   [sha256.Size]byte
}

func newTokens(cfg []config.APIToken) []token {
	out := make([]token, 0, len(cfg))
	for _, t := range cfg {
		out = append(out, token{
			name: t.Name, botID: t.BotInstanceID, scopes: slices.Clone(t.Scopes), hash: sha256.Sum256([]byte(t.Token)),
		})
	}
	return out
}

// authenticate returns the token presented as "Authorization: Bearer <token>".
// Every configured hash is compared in constant time.
func (s *Server) authenticate(r *http.Request) *token {
	scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	raw = strings.TrimSpace(raw)
	if !ok || !strings.EqualFold(scheme, "Bearer") || raw == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(raw))
	var found *token
	for i := range s.tokens {
		if subtle.ConstantTimeCompare(sum[:], s.tokens[i].hash[:]) == 1 && found == nil {
			found = &s.tokens[i]
		}
	}
	return found
}

type authedHandler func(w http.ResponseWriter, r *http.Request, tok *token)

// handle registers h behind authentication; a non-empty scope must be held by
// the token.
func (s *Server) handle(pattern, scope string, h authedHandler) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		tok := s.authenticate(r)
		if tok == nil {
			s.writeError(w, r, http.StatusUnauthorized, conloomapi.CodeUnauthorized, "missing or invalid bearer token", false, nil)
			return
		}
		if scope != "" && !slices.Contains(tok.scopes, scope) {
			s.writeError(w, r, http.StatusForbidden, conloomapi.CodeForbidden, "token lacks scope "+scope, false, nil)
			return
		}
		h(w, r, tok)
	})
}
