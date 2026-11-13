package panel

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

// static serves a file from Assets: index.html for /panel/, 404 for
// directories and paths that are not valid fs paths. The URLs are not
// versioned, so browsers must revalidate every response after an upgrade.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
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
		http.NotFound(w, r)
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
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("panel asset unreadable", "trace_id", traceID(r.Context()), "name", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(data))
}
