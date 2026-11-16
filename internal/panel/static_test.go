package panel

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		"export const del = (path) => request('DELETE', path);",
		"export const upload = (path, blob, params) => request('POST', path, { blob, params });",
	} {
		if !strings.Contains(api, want) {
			t.Errorf("/panel/lib/api.js lacks %q", want)
		}
	}
}

func TestStatic_TemplatesCreateAndDelete(t *testing.T) {
	h := staticHandler(t, web.FS)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panel/views/templates.js", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/panel/views/templates.js: %d", w.Code)
	}
	js := w.Body.String()
	for _, want := range []string{
		// 新建模板: a name under the rules of a new template, started from
		// the built-in default persona or a copy of a template, never
		// overwriting one.
		"'新建模板')", "const BUILTIN_DEFAULT = 'persona-templates/default/builtin';",
		"const RESERVED_NAMES = ['export', 'import'];", "RESERVED_NAMES.includes(name)",
		"'空白（内置默认人设）'", "`复制 ${t.name}`", "(await get(BUILTIN_DEFAULT)).persona",
		"if (await exists(to)) throw new Error(", "await createTemplate(to, persona,", "go(href('templates', to))",
		// 复制为新模板 stays, under the same name rules.
		"'复制为新模板')", "const problem = nameProblem(to);",
		// Deleting names the bots that use the template, before and after.
		"(t.users || []).map(botLabel)", "删除后改用 default", "res.affected_bots", "改用 default：",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("/panel/views/templates.js lacks %q", want)
		}
	}
	for _, gone := range []string{"{ title: '默认模板用于'", "新建模板：打开一个模板", "TEMPLATE_IN_USE"} {
		if strings.Contains(js, gone) {
			t.Errorf("/panel/views/templates.js still has %q", gone)
		}
	}
}
