package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/panel"
	"github.com/kinako404/conloom/internal/panel/web"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	panelUser     = "admin"
	panelPassword = "fake-panel-password"
	adminToken    = "fake-admin-token"
)

// panelTestConfig is testConfig with a panel section whose one user is
// panelUser, and an admin:read token adminToken.
func panelTestConfig(t *testing.T, p configParams, hash string) *config.Config {
	t.Helper()
	cfg := testConfig(t, p)
	cfg.Panel = &config.PanelConfig{
		Users:       []config.PanelUser{{Name: panelUser, PasswordHash: hash}},
		SessionTTL:  conloomapi.Duration{Duration: 24 * time.Hour},
		IdleTimeout: conloomapi.Duration{Duration: time.Hour},
	}
	cfg.APITokens = append(cfg.APITokens, config.APIToken{
		Name: "admin", Token: adminToken, BotInstanceID: botID, Scopes: []string{conloomapi.ScopeAdminRead},
	})
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	return cfg
}

// panelClient calls the panel API of a test server as a logged-in user.
type panelClient struct {
	t      *testing.T
	base   string
	cookie *http.Cookie
	csrf   string
}

func panelLogin(t *testing.T, base string) *panelClient {
	t.Helper()
	body, err := json.Marshal(map[string]string{"name": panelUser, "password": panelPassword})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(base+"/panel/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d, body %s", resp.StatusCode, raw)
	}
	var sess struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(raw, &sess); err != nil || sess.CSRFToken == "" {
		t.Fatalf("login response %s: %v", raw, err)
	}
	c := &panelClient{t: t, base: base, csrf: sess.CSRFToken}
	for _, ck := range resp.Cookies() {
		if ck.Name == "conloom_panel" {
			c.cookie = ck
		}
	}
	if c.cookie == nil {
		t.Fatalf("login set no session cookie: %v", resp.Header.Values("Set-Cookie"))
	}
	return c
}

// do sends a request with the session cookie and, unless it is a GET, the
// CSRF token and body as JSON; it returns the status and response body.
func (c *panelClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.AddCookie(c.cookie)
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", c.csrf)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

// get requires GET path to succeed, decodes its body into out and returns the
// raw body.
func (c *panelClient) get(path string, out any) []byte {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, path, nil)
	if status != http.StatusOK {
		c.t.Fatalf("GET %s: status %d, body %s", path, status, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		c.t.Fatalf("GET %s: decode %s: %v", path, raw, err)
	}
	return raw
}

// todayChatUsage returns today's chat row of the usage report by purpose.
func (c *panelClient) todayChatUsage() (usage.Row, bool) {
	c.t.Helper()
	var rep usage.Report
	c.get("/panel/api/usage?group=purpose", &rep)
	if len(rep.Days) == 0 || rep.Days[len(rep.Days)-1].Day != rep.To {
		c.t.Fatalf("usage report days = %+v, want the last to be today %s", rep.Days, rep.To)
	}
	for _, row := range rep.Days[len(rep.Days)-1].Rows {
		if row.Key == "chat" {
			return row, true
		}
	}
	return usage.Row{}, false
}

// fakePromptTokens counts the prompt tokens fakeopenai reports for req.
func fakePromptTokens(req map[string]any) int64 {
	var n int64
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			s, _ := mm["content"].(string)
			n += int64(utf8.RuneCountInString(s))
		}
	}
	return n
}

func TestPanelE2E(t *testing.T) {
	const script = "<script>alert(1)</script>"
	fake := fakeopenai.New(t, answer(fakeReply))
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	e1 := newEnv(t, panelTestConfig(t, configParams{dataDir: dataDir, modelURL: fake.URL}, hash))

	p1 := panelLogin(t, e1.srv.URL)
	e1.mention(group, alice, "在吗？")
	e1.drainOne()
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(reqs))
	}
	want := usage.Row{Key: "chat", Calls: 1, PromptTokens: fakePromptTokens(reqs[0]),
		CompletionTokens: int64(utf8.RuneCountInString(fakeReply))}
	var row usage.Row
	waitUntil(t, "the chat call in today's usage", func() bool {
		var ok bool
		row, ok = p1.todayChatUsage()
		return ok
	})
	if row.Calls != want.Calls || row.Errors != 0 || row.PromptTokens != want.PromptTokens ||
		row.CompletionTokens != want.CompletionTokens {
		t.Fatalf("today's chat usage = %+v, want %+v", row, want)
	}

	// The mock numbers its messages from 1 again after the restart, so the
	// message with HTML is sent now.
	if resp, _ := e1.say(group, bob, script, mock.SayOptions{}); resp.Disposition != conloomapi.DispositionObserve {
		t.Fatalf("message with HTML: response %+v, want observe", resp)
	}
	e1.close()
	e2 := newEnv(t, panelTestConfig(t, configParams{dataDir: dataDir, modelURL: fake.URL}, hash))
	p2 := panelLogin(t, e2.srv.URL)
	if got, ok := p2.todayChatUsage(); !ok || got.Calls != want.Calls || got.PromptTokens != want.PromptTokens {
		t.Fatalf("today's chat usage after restart = %+v (found %v), want %+v", got, ok, want)
	}

	var convs struct {
		Conversations []struct {
			Key                    string `json:"key"`
			Type                   string `json:"type"`
			PlatformConversationID string `json:"platform_conversation_id"`
		} `json:"conversations"`
	}
	p2.get("/panel/api/conversations", &convs)
	var key string
	for _, c := range convs.Conversations {
		if c.Type == conloomapi.ConvGroup && c.PlatformConversationID == group.PlatformID {
			key = c.Key
		}
	}
	if key == "" {
		t.Fatalf("conversations = %+v, want group %s", convs.Conversations, group.PlatformID)
	}
	convPath := "/panel/api/conversations/" + url.PathEscape(key)

	var pol struct {
		Current struct {
			Policy conloomapi.GroupPolicy `json:"policy"`
		} `json:"current"`
	}
	p2.get(convPath+"/policy", &pol)
	if !pol.Current.Policy.Proactive.Enabled {
		t.Fatal("proactive already off before the panel change")
	}
	gp := pol.Current.Policy
	gp.Proactive.Enabled = false
	if status, raw := p2.do(http.MethodPut, convPath+"/policy", map[string]any{"policy": gp, "reason": "关闭主动发言"}); status != http.StatusOK {
		t.Fatalf("PUT policy: status %d, body %s", status, raw)
	}
	state, err := client.New(e2.srv.URL, adminToken, nil).GetPolicy(context.Background(), "group_10001")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if state.Policy.Proactive.Enabled || state.UpdatedBy != "panel:"+panelUser {
		t.Fatalf("policy through the Bot Interaction API = %+v, want proactive off, updated by panel:%s", state, panelUser)
	}

	var audit struct {
		Events []struct {
			Kind  string `json:"kind"`
			Actor string `json:"actor"`
		} `json:"events"`
	}
	p2.get("/panel/api/audit", &audit)
	kinds := map[string]bool{}
	for _, ev := range audit.Events {
		if ev.Actor == "panel:"+panelUser {
			kinds[ev.Kind] = true
		}
	}
	if !kinds["panel.login"] || !kinds["policy.update"] {
		t.Fatalf("audit events = %+v, want panel.login and policy.update by panel:%s", audit.Events, panelUser)
	}

	var cfgView map[string]any
	raw := p2.get("/panel/api/config", &cfgView)
	for _, secret := range []string{adapterToken, adminToken, "fake-model-key"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("config view contains the secret %q", secret)
		}
	}

	var msgs struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
	}
	raw = p2.get(convPath+"/messages", &msgs)
	found := false
	for _, m := range msgs.Messages {
		found = found || m.Text == script
	}
	if !found {
		t.Errorf("messages = %+v, want %q verbatim", msgs.Messages, script)
	}
	if bytes.Contains(raw, []byte("<script")) || !bytes.Contains(raw, []byte(`\u003cscript\u003ealert(1)\u003c/script\u003e`)) {
		t.Errorf("messages body does not escape < and >: %s", raw)
	}

	resp, err := http.Get(e2.srv.URL + "/panel/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	index, err := fs.ReadFile(web.FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(page, index) ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET /panel/: status %d, type %q, body is the embedded index.html: %v",
			resp.StatusCode, resp.Header.Get("Content-Type"), bytes.Equal(page, index))
	}
}

func TestPanelDisabledWithoutSection(t *testing.T) {
	e := newEnv(t, testConfig(t, configParams{modelURL: "http://127.0.0.1:1"}))
	for _, path := range []string{"/panel", "/panel/", "/panel/api/session"} {
		resp, err := http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s without a panel section: status %d, want 404", path, resp.StatusCode)
		}
	}
}
