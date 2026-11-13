package panel

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
)

func TestStatic_ServesIndexAndRedirects(t *testing.T) {
	root := fstest.MapFS{
		"web/index.html":   {Data: []byte("<!doctype html><title>织语面板</title>")},
		"web/app.js":       {Data: []byte("import './api.js';")},
		"web/style.css":    {Data: []byte("body{}")},
		"web/views/a.js":   {Data: []byte("export {};")},
		"config":           {Data: []byte("outside-the-web-root")},
		"web/views/README": {Data: []byte("x")},
	}
	assets, err := fs.Sub(root, "web")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{
		Config: &config.Config{Panel: &config.PanelConfig{
			Users:       []config.PanelUser{{Name: testUser, PasswordHash: testHash(t)}},
			SessionTTL:  dur(config.DefaultPanelSessionTTL),
			IdleTimeout: dur(config.DefaultPanelIdleTimeout),
		}},
		Sessions: &fakeSessions{m: map[string]store.PanelSession{}},
		Audit:    &fakeAudit{},
		Assets:   assets,
		Metrics:  obs.NewMetrics(prometheus.NewRegistry()),
		Now:      func() time.Time { return time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC) },
	})
	h := srv.Handler()
	get := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}

	w := get("/panel")
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/panel/" {
		t.Errorf("/panel: %d Location %q, want 301 to /panel/", w.Code, w.Header().Get("Location"))
	}

	w = get("/panel/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "织语面板") {
		t.Errorf("/panel/: %d %q, want index.html", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("/panel/ Content-Type = %q", ct)
	}

	w = get("/panel/app.js")
	if ct := w.Header().Get("Content-Type"); w.Code != http.StatusOK || !strings.Contains(ct, "javascript") {
		t.Errorf("/panel/app.js: %d Content-Type %q", w.Code, ct)
	}
	if w = get("/panel/views/a.js"); w.Code != http.StatusOK || w.Body.String() != "export {};" {
		t.Errorf("/panel/views/a.js: %d %q", w.Code, w.Body)
	}
	if w = get("/panel/index.html"); w.Code != http.StatusOK {
		t.Errorf("/panel/index.html: %d", w.Code)
	}
	for _, p := range []string{"/panel/", "/panel/index.html", "/panel/app.js", "/panel/style.css", "/panel/views/a.js"} {
		if got := get(p).Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", p, got)
		}
	}

	for _, p := range []string{"/panel/missing.js", "/panel/views/", "/panel/views", "/panel/views/missing.js"} {
		if w := get(p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}

	for _, p := range []string{"/panel/../config", "/panel/%2e%2e/config", "/panel/..%2fconfig", "/panel/views/../../config", "/panel//config"} {
		w := get(p)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "outside-the-web-root") {
			t.Errorf("%s: %d %q escaped the assets", p, w.Code, w.Body)
		}
	}

	r := httptest.NewRequest(http.MethodPost, "http://"+testHost+"/panel/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /panel/app.js: %d, want 405", rec.Code)
	}
}
