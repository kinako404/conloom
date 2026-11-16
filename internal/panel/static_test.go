package panel

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/panel/web"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
)

// staticHandler is a panel serving assets, with no data behind its API.
func staticHandler(t *testing.T, assets fs.FS) http.Handler {
	t.Helper()
	return loggingStaticHandler(t, assets, nil)
}

// loggingStaticHandler is staticHandler logging to logger.
func loggingStaticHandler(t *testing.T, assets fs.FS, logger *slog.Logger) http.Handler {
	t.Helper()
	srv := New(Deps{
		Logger: logger,
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
	return srv.Handler()
}

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
	h := staticHandler(t, assets)
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

	for _, p := range []string{"/panel/missing.js", "/panel/views/missing.js", "/panel/favicon.ico", "/panel/views/a.js/x.css"} {
		if w := get(p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
	if w = get("/panel/views/README"); w.Code != http.StatusOK || w.Body.String() != "x" {
		t.Errorf("/panel/views/README: %d %q, want the asset", w.Code, w.Body)
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

// redirectAssets is a web root for the redirect tests.
var redirectAssets = fstest.MapFS{
	"index.html":   {Data: []byte("<!doctype html>")},
	"app.js":       {Data: []byte("export {};")},
	"views/a.js":   {Data: []byte("export {};")},
	"views/README": {Data: []byte("x")},
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestStatic_FragmentInPathRedirectsToTheRoute(t *testing.T) {
	h := staticHandler(t, redirectAssets)
	for _, c := range []struct{ target, location string }{
		{"/panel/%23/login?next=%23%2F", "/panel/#/login?next=%23%2F"},
		{"/panel/%23/", "/panel/#/"},
		{"/panel/%23", "/panel/#"},
		{"/panel/%23/conversations/bot%3Aqq%2Fx/memory", "/panel/#/conversations/bot%3Aqq%2Fx/memory"},
		{"/panel/%23/chat?", "/panel/#/chat"},
		{"/panel/views/%23/usage?days=7", "/panel/#/usage?days=7"},
		{"/panel/app.js%23/x", "/panel/#/x"},
		{"/panel/%23/a%23b", "/panel/#/a%23b"},
		{"/panel/%23//evil.example/", "/panel/#//evil.example/"},
		{"/panel/%23/a%0D%0ASet-Cookie:%20x=1", "/panel/#/a%0D%0ASet-Cookie:%20x=1"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := serve(h, httptest.NewRequest(method, c.target, nil))
			if w.Code != http.StatusFound || w.Header().Get("Location") != c.location {
				t.Errorf("%s %s: %d Location %q, want 302 to %q", method, c.target, w.Code, w.Header().Get("Location"), c.location)
			}
			if w.Header().Get("Set-Cookie") != "" {
				t.Errorf("%s %s set a cookie", method, c.target)
			}
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/panel/%23/x", nil)
	r.URL.RawQuery = "a=1\r\nSet-Cookie: x=1\n\xff"
	w := serve(h, r)
	if loc := w.Header().Get("Location"); w.Code != http.StatusFound || loc != "/panel/#/x?a=1Set-Cookie:%20x=1%FF" {
		t.Errorf("query with CR/LF: %d Location %q", w.Code, loc)
	}

	r = httptest.NewRequest(http.MethodPost, "http://"+testHost+"/panel/%23/login", nil)
	if w := serve(h, r); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /panel/%%23/login: %d, want 405", w.Code)
	}
}

func TestStatic_PagePathsRedirectToTheApp(t *testing.T) {
	h := staticHandler(t, redirectAssets)
	for _, target := range []string{
		"/panel/login", "/panel/login/", "/panel/conversations/x/memory", "/panel/views", "/panel/views/",
		"/panel/app.js/", "/panel/v1.2/chat", "/panel/..%2fconfig",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := serve(h, httptest.NewRequest(method, target, nil))
			if w.Code != http.StatusFound || w.Header().Get("Location") != "/panel/" {
				t.Errorf("%s %s: %d Location %q, want 302 to /panel/", method, target, w.Code, w.Header().Get("Location"))
			}
			if got := w.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("%s %s: Cache-Control = %q, want no-cache", method, target, got)
			}
		}
	}
	for _, target := range []string{"/panel/missing.js", "/panel/favicon.ico", "/panel/views/missing.css", "/panel/.well-known"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if w := serve(h, httptest.NewRequest(method, target, nil)); w.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d, want 404", method, target, w.Code)
			}
		}
	}
	if w := serve(h, httptest.NewRequest(http.MethodGet, "/panel/views/README", nil)); w.Code != http.StatusOK {
		t.Errorf("/panel/views/README: %d, want the asset", w.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "http://"+testHost+"/panel/login", nil)
	if w := serve(h, r); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /panel/login: %d, want 405", w.Code)
	}
}

func TestHandler_CaseVariantsOfThePrefixRedirect(t *testing.T) {
	h := staticHandler(t, redirectAssets)
	for _, c := range []struct{ target, location string }{
		{"/Panel", "/panel"},
		{"/PANEL", "/panel"},
		{"/PANEL/", "/panel/"},
		{"/Panel/app.js?v=1", "/panel/app.js?v=1"},
		{"/PaNeL/api/session", "/panel/api/session"},
		{"/Panel/%23/login?next=%23%2F", "/panel/%23/login?next=%23%2F"},
		{"/PANEL//evil.example", "/panel//evil.example"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := serve(h, httptest.NewRequest(method, c.target, nil))
			if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != c.location {
				t.Errorf("%s %s: %d Location %q, want 301 to %q", method, c.target, w.Code, w.Header().Get("Location"), c.location)
			}
			if w.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("%s %s: no Content-Security-Policy", method, c.target)
			}
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/PANEL/x", nil)
	r.URL.RawQuery = "a\r\nb"
	if w := serve(h, r); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/panel/x?ab" {
		t.Errorf("query with CR/LF: %d Location %q", w.Code, w.Header().Get("Location"))
	}

	for _, target := range []string{"/panelx", "/PANELX", "/Panel.js", "/x/PANEL/"} {
		if w := serve(h, httptest.NewRequest(http.MethodGet, target, nil)); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", target, w.Code)
		}
	}
	r = httptest.NewRequest(http.MethodPost, "http://"+testHost+"/PANEL/api/login", nil)
	if w := serve(h, r); w.Code != http.StatusNotFound {
		t.Errorf("POST /PANEL/api/login: %d, want 404", w.Code)
	}
}

func TestHandler_LogsNotFound(t *testing.T) {
	var logs bytes.Buffer
	h := loggingStaticHandler(t, redirectAssets, slog.New(slog.NewJSONHandler(&logs, nil)))
	ua := strings.Repeat("u", 150) + strings.Repeat("界", 100)
	for _, c := range []struct{ method, target string }{
		{http.MethodGet, "/panel/missing.js?token=s3cret"},
		{http.MethodHead, "/panel/favicon.ico"},
		{http.MethodGet, "/panel/api/nope"},
		{http.MethodGet, "/nope"},
		{http.MethodGet, "/panel/"},
		{http.MethodGet, "/panel/login"},
		{http.MethodGet, "/panel/%23/login"},
		{http.MethodGet, "/PANEL/"},
		{http.MethodGet, "/panel/app.js"},
	} {
		r := httptest.NewRequest(c.method, c.target, nil)
		r.Header.Set("User-Agent", ua)
		r.RemoteAddr = "198.51.100.7:4242"
		serve(h, r)
	}

	if strings.Contains(logs.String(), "s3cret") {
		t.Errorf("the query was logged: %s", logs.String())
	}
	var got []string
	dec := json.NewDecoder(&logs)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		if m["msg"] != "panel not found" {
			continue
		}
		got = append(got, m["method"].(string)+" "+m["path"].(string))
		if m["level"] != "INFO" {
			t.Errorf("%v: level %v, want INFO", m["path"], m["level"])
		}
		if want := strings.Repeat("u", 150) + strings.Repeat("界", 50); m["user_agent"] != want {
			t.Errorf("%v: user_agent %q, want the first 200 characters", m["path"], m["user_agent"])
		}
		if m["remote"] != "198.51.100.7:4242" {
			t.Errorf("%v: remote %v", m["path"], m["remote"])
		}
	}
	want := []string{"GET /panel/missing.js", "HEAD /panel/favicon.ico", "GET /panel/api/nope", "GET /nope"}
	if !slices.Equal(got, want) {
		t.Errorf("logged %q, want %q", got, want)
	}
}

func TestStatic_ServesAutonomyView(t *testing.T) {
	h := staticHandler(t, web.FS)
	get := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}
	w := get("/panel/views/autonomy.js")
	if ct := w.Header().Get("Content-Type"); w.Code != http.StatusOK || !strings.Contains(ct, "javascript") {
		t.Fatalf("/panel/views/autonomy.js: %d Content-Type %q", w.Code, ct)
	}
	if !strings.Contains(w.Body.String(), "export async function autonomyTab(") {
		t.Error("/panel/views/autonomy.js does not export autonomyTab")
	}
	if w = get("/panel/views/conversation.js"); !strings.Contains(w.Body.String(), "from './autonomy.js'") {
		t.Errorf("/panel/views/conversation.js: %d, does not import ./autonomy.js", w.Code)
	}
}

func TestStatic_ServesChatsView(t *testing.T) {
	h := staticHandler(t, web.FS)
	get := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}
	w := get("/panel/views/chats.js")
	if ct := w.Header().Get("Content-Type"); w.Code != http.StatusOK || !strings.Contains(ct, "javascript") {
		t.Fatalf("/panel/views/chats.js: %d Content-Type %q", w.Code, ct)
	}
	for _, want := range []string{"export async function chatsView(", "export function newChatDialog("} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("/panel/views/chats.js lacks %q", want)
		}
	}
	app := get("/panel/app.js").Body.String()
	for _, want := range []string{"from './views/chats.js'", "route('/chat', chatsView)", "route('/chat/:key', chatsView)", "['chat', '聊天']"} {
		if !strings.Contains(app, want) {
			t.Errorf("/panel/app.js lacks %q", want)
		}
	}
	if w = get("/panel/views/conversations.js"); !strings.Contains(w.Body.String(), "from './chats.js'") {
		t.Errorf("/panel/views/conversations.js: %d, does not import ./chats.js", w.Code)
	}
	w = get("/panel/views/conversation.js")
	if strings.Contains(w.Body.String(), "['chat', '聊天']") {
		t.Error("/panel/views/conversation.js still has the chat tab")
	}
	if !strings.Contains(w.Body.String(), "location.replace(href('chat', key))") {
		t.Error("/panel/views/conversation.js does not send old chat tab links to the chat page")
	}
	for _, view := range []string{"chats.js", "chat.js", "conversation.js"} {
		if body := get("/panel/views/" + view).Body.String(); strings.Contains(body, "接管") || strings.Contains(body, "takeover") {
			t.Errorf("/panel/views/%s still offers the takeover of platform chats", view)
		}
	}
}

func TestStatic_SharedMemoryNote(t *testing.T) {
	h := staticHandler(t, web.FS)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panel/views/conversation.js", nil))
	body := w.Body.String()
	for _, want := range []string{
		"'这个 bot 的所有面板聊天共用这份记忆'",
		"const SCOPE_TABS = new Set(['memory', 'autonomy']);",
		"c.memory_scope === c.key",
		"mount(body, scopeNote(conv, tab), nodes)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/panel/views/conversation.js lacks %q", want)
		}
	}
}

func TestStatic_ServesTemplatesViews(t *testing.T) {
	h := staticHandler(t, web.FS)
	get := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}
	for _, c := range []struct {
		path string
		want []string
	}{
		{"/panel/views/templates.js", []string{
			"export async function templatesView(", "export async function templateView(", "from './personaform.js'",
		}},
		{"/panel/views/personaform.js", []string{"export function personaForm(", "upload("}},
		{"/panel/views/conversation.js", []string{"from './personaform.js'"}},
		{"/panel/lib/api.js", []string{"export const upload ="}},
		{"/panel/app.js", []string{
			"from './views/templates.js'", "route('/templates', templatesView)", "route('/templates/:name', templateView)",
			"['templates', '人设模板']",
		}},
	} {
		w := get(c.path)
		if ct := w.Header().Get("Content-Type"); w.Code != http.StatusOK || !strings.Contains(ct, "javascript") {
			t.Errorf("%s: %d Content-Type %q", c.path, w.Code, ct)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(w.Body.String(), want) {
				t.Errorf("%s lacks %q", c.path, want)
			}
		}
	}
	if w := get("/panel/views/conversation.js"); strings.Contains(w.Body.String(), "function barsEditor(") {
		t.Error("/panel/views/conversation.js still has its own bars editor")
	}
}

func TestStatic_TemplatesExportImport(t *testing.T) {
	h := staticHandler(t, web.FS)
	get := func(target string) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", target, w.Code)
		}
		return w.Body.String()
	}
	js := get("/panel/views/templates.js")
	for _, want := range []string{
		// Downloads are same-origin links the browser saves.
		"const EXPORT_ALL = 'api/persona-templates/export';",
		"href: 'api/' + templatePath(name, 'export'), download: true",
		"href: EXPORT_ALL, download: true }, '导出全部')",
		"}, '导出')",
		// Import: the file as the raw body, first with fail, then the dialog.
		"upload('persona-templates/import', body, { on_conflict: mode })",
		"await f.text()", "type: 'application/json'", "send(body, 'fail')",
		"const TEMPLATE_EXISTS = 'TEMPLATE_EXISTS';", "err.names",
		"'覆盖')", "'改名导入')", "'取消')", "choose('overwrite')", "choose('rename')",
		"新建：", "覆盖：", "可能超过这个上限",
		// Esc does not close the dialog of a running import; if the browser
		// closes it anyway, the page shows the progress and the error.
		"if (busy) e.preventDefault();", "dialog.addEventListener('close'", "mount(dialog.open ? status : msg, errorBox(err))",
		"{ title: '导出', class: 'nowrap', cell: (t) => exportLink(t.name) }",
		"'恢复为内置默认')", "'内置'",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("/panel/views/templates.js lacks %q", want)
		}
	}
	for _, gone := range []string{"恢复为配置文件版本", "配置文件", "数据库", "fetch("} {
		if strings.Contains(js, gone) {
			t.Errorf("/panel/views/templates.js still has %q", gone)
		}
	}
	m := regexp.MustCompile(`const MAX_IMPORT_MIB = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("/panel/views/templates.js has no MAX_IMPORT_MIB")
	}
	if want := strconv.Itoa(maxImportBytes >> 20); m[1] != want {
		t.Errorf("MAX_IMPORT_MIB = %s, want maxImportBytes in MiB %s", m[1], want)
	}
	m = regexp.MustCompile(`const MAX_IMPORT_TEMPLATES = (\d+);`).FindStringSubmatch(js)
	if want := strconv.Itoa(persona.MaxExchangeTemplates); m == nil || m[1] != want {
		t.Errorf("MAX_IMPORT_TEMPLATES = %v, want persona.MaxExchangeTemplates %s", m, want)
	}

	api := get("/panel/lib/api.js")
	for _, want := range []string{
		"this.names = names || [];", "res.headers.get('Retry-After'), e.names)",
		"export const upload = (path, blob, params) => request('POST', path, { blob, params });",
	} {
		if !strings.Contains(api, want) {
			t.Errorf("/panel/lib/api.js lacks %q", want)
		}
	}
}
