package panel

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// maxLoggedUserAgent and maxLoggedPath are how many characters of the
// User-Agent header and of the path a "panel not found" log line keeps.
const (
	maxLoggedUserAgent = 200
	maxLoggedPath      = 512
)

// static serves a file from Assets: index.html for /panel/. A path that is
// not a file of Assets (a directory, a missing file or not a valid fs path)
// is redirected to /panel/ when its last segment has no extension, so it
// looks like a page of the app, and is not found otherwise. The URLs are not
// versioned, so browsers must revalidate every response after an upgrade.
// A path under /panel/api/ that reaches static, because its slash after api
// was escaped (/panel/api%2F...), is an unknown API.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/panel/api/") {
		s.noSuchAPI(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/panel/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		s.missingAsset(w, r, name)
		return
	}
	info, err := fs.Stat(s.d.Assets, name)
	if err == nil && info.IsDir() {
		err = fs.ErrNotExist
	}
	var data []byte
	if err == nil {
		data, err = fs.ReadFile(s.d.Assets, name)
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
		s.missingAsset(w, r, name)
		return
	}
	if err != nil {
		s.log.Error("panel asset unreadable", "trace_id", traceID(r.Context()), "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(data))
}

// missingAsset answers a request for name, which is not a file of Assets.
func (s *Server) missingAsset(w http.ResponseWriter, r *http.Request, name string) {
	if path.Ext(name) == "" {
		http.Redirect(w, r, "/panel/", http.StatusFound)
		return
	}
	s.notFound(w, r)
}

// redirectMangledURL answers a GET or HEAD request whose URL was mangled on
// its way to the panel and reports whether it did. Some mobile browsers and
// in-app webviews send the fragment of /panel/#/route?q as the path
// /panel/%23/route?q; such a path under /panel/ outside its API is redirected
// to /panel/# followed by what follows the first # in the path, still
// escaped, and the query. A path whose first segment is /panel in another
// case is redirected to the same path and query under /panel.
func redirectMangledURL(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p, escaped := r.URL.Path, r.URL.EscapedPath()
	if strings.HasPrefix(p, "/panel/") && !strings.HasPrefix(p, "/panel/api/") && strings.Contains(p, "#") {
		if i := strings.Index(escaped, "%23"); i >= 0 {
			redirectWithQuery(w, r, "/panel/#"+escaped[i+len("%23"):], http.StatusFound)
			return true
		}
	}
	const prefix = "/panel"
	if len(escaped) >= len(prefix) && strings.EqualFold(escaped[:len(prefix)], prefix) && escaped[:len(prefix)] != prefix &&
		(len(escaped) == len(prefix) || escaped[len(prefix)] == '/') {
		redirectWithQuery(w, r, prefix+escaped[len(prefix):], http.StatusMovedPermanently)
		return true
	}
	return false
}

// redirectWithQuery redirects to location, a path under /panel, followed by
// the query of r. Unlike http.Redirect it does not clean the path, which would
// change a fragment. CR and LF are dropped; spaces, control characters and
// bytes outside ASCII are escaped.
func redirectWithQuery(w http.ResponseWriter, r *http.Request, location string, code int) {
	if r.URL.RawQuery != "" {
		location += "?" + r.URL.RawQuery
	}
	var b strings.Builder
	for i := 0; i < len(location); i++ {
		switch c := location[i]; {
		case c == '\r' || c == '\n':
		case c <= ' ' || c >= 0x7f:
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	w.Header().Set("Location", b.String())
	w.WriteHeader(code)
}

// notFound answers 404 and logs it.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.logNotFound(r)
	http.NotFound(w, r)
}

// logNotFound logs a 404 answer to r. The query is left out.
func (s *Server) logNotFound(r *http.Request) {
	s.log.Info("panel not found", "trace_id", traceID(r.Context()),
		"path", truncateRunes(r.URL.Path, maxLoggedPath), "method", r.Method,
		"user_agent", truncateRunes(r.UserAgent(), maxLoggedUserAgent), "remote", r.RemoteAddr)
}

// truncateRunes returns the first n characters of s.
func truncateRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}
