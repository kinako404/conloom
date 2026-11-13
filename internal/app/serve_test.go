package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/panel"
)

const serveTestConfig = `
server:
  listen: 127.0.0.1:0
api_tokens:
  - {name: adapter, token: fake-adapter-token, bot_instance_id: bot_main, scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]}
bots:
  - id: bot_main
    platform: qq
    platform_self_id: "30001"
    admins: ["29999"]
    enabled_conversations:
      - {type: group, platform_id: "10001"}
model_profiles:
  short_context:
    endpoint: http://127.0.0.1:9/v1
    model: fake-model
    context: {native_window: 32768, configured_window: 32768, soft_input_budget: 16000}
storage:
  data_dir: DATA_DIR
`

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var (
	listeningAddr      = regexp.MustCompile(`msg=listening addr=(\S+)`)
	panelListeningAddr = regexp.MustCompile(`msg="panel listening" addr=(\S+)`)
)

// loadServeConfig writes yml, with DATA_DIR replaced by a temporary
// directory, to a file and loads and validates it.
func loadServeConfig(t *testing.T, yml string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "conloom.yaml")
	yml = strings.ReplaceAll(yml, "DATA_DIR", strconv.Quote(filepath.Join(dir, "data")))
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	return cfg
}

// startServe runs Serve in the background and waits for the address each of
// patterns finds in the logs.
func startServe(t *testing.T, cfg *config.Config, patterns ...*regexp.Regexp) (addrs []string, cancel context.CancelFunc, served <-chan error) {
	t.Helper()
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, logger, Options{}) }()

	for deadline := time.Now().Add(5 * time.Second); ; {
		addrs = addrs[:0]
		for _, p := range patterns {
			if m := p.FindStringSubmatch(logs.String()); m != nil {
				addrs = append(addrs, m[1])
			}
		}
		if len(addrs) == len(patterns) {
			return addrs, cancel, done
		}
		select {
		case err := <-done:
			t.Fatalf("Serve returned early: %v\nlogs:\n%s", err, logs)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start; logs:\n%s", logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitServed(t *testing.T, served <-chan error) error {
	t.Helper()
	select {
	case err := <-served:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after shutdown")
		return nil
	}
}

func TestServe_ShutdownEndsLongPollPromptly(t *testing.T) {
	cfg := loadServeConfig(t, serveTestConfig)
	addrs, cancel, served := startServe(t, cfg, listeningAddr)
	addr := addrs[0]

	type pollResult struct {
		status int
		body   string
		err    error
	}
	polled := make(chan pollResult, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/v1/outbox?wait=30s", nil)
		req.Header.Set("Authorization", "Bearer fake-adapter-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			polled <- pollResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		polled <- pollResult{status: resp.StatusCode, body: string(body), err: err}
	}()
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	cancel()
	if err := waitServed(t, served); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("shutdown took %v with a long poll in flight, want prompt", elapsed)
	}
	r := <-polled
	if r.err != nil || r.status != http.StatusOK || strings.TrimSpace(r.body) != `{"actions":[]}` {
		t.Fatalf("long poll during shutdown: status %d, body %q, err %v; want 200 {\"actions\":[]}", r.status, r.body, r.err)
	}
}

const servePanelPassword = "fake-panel-password"

// panelServeConfig is serveTestConfig with a panel section listening on
// panelListen.
func panelServeConfig(t *testing.T, panelListen string) *config.Config {
	t.Helper()
	hash, err := panel.HashPassword(servePanelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	return loadServeConfig(t, serveTestConfig+`
panel:
  listen: `+panelListen+`
  users: [{name: admin, password_hash: "`+hash+`"}]
`)
}

// noRedirect is a client that returns redirects instead of following them.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func request(t *testing.T, method, url, body string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func TestServe_PanelOnItsOwnListener(t *testing.T) {
	cfg := panelServeConfig(t, "127.0.0.1:0")
	addrs, cancel, served := startServe(t, cfg, listeningAddr, panelListeningAddr)
	core, site := "http://"+addrs[0], "http://"+addrs[1]
	if core == site {
		t.Fatalf("core and panel share %s", core)
	}
	bearer := map[string]string{"Authorization": "Bearer fake-adapter-token"}
	login := `{"name":"admin","password":"` + servePanelPassword + `"}`
	jsonBody := map[string]string{"Content-Type": "application/json"}

	for _, tc := range []struct {
		name, method, url, body string
		header                  map[string]string
		status                  int
		location                string
	}{
		{"panel page", http.MethodGet, site + "/panel/", "", nil, http.StatusOK, ""},
		{"panel bare path", http.MethodGet, site + "/panel", "", nil, http.StatusMovedPermanently, "/panel/"},
		{"root redirect", http.MethodGet, site + "/", "", nil, http.StatusFound, "/panel/"},
		{"panel login", http.MethodPost, site + "/panel/api/login", login, jsonBody, http.StatusOK, ""},
		{"panel session", http.MethodGet, site + "/panel/api/session", "", nil, http.StatusUnauthorized, ""},
		{"no metrics on panel", http.MethodGet, site + "/metrics", "", nil, http.StatusNotFound, ""},
		{"no healthz on panel", http.MethodGet, site + "/healthz", "", nil, http.StatusNotFound, ""},
		{"no api on panel", http.MethodGet, site + "/api/v1/capabilities", "", bearer, http.StatusNotFound, ""},
		{"no outbox on panel", http.MethodGet, site + "/api/v1/outbox", "", bearer, http.StatusNotFound, ""},
		{"no panel on core", http.MethodGet, core + "/panel/", "", nil, http.StatusNotFound, ""},
		{"no panel bare path on core", http.MethodGet, core + "/panel", "", nil, http.StatusNotFound, ""},
		{"no panel login on core", http.MethodPost, core + "/panel/api/login", login, jsonBody, http.StatusNotFound, ""},
		{"no root redirect on core", http.MethodGet, core + "/", "", nil, http.StatusNotFound, ""},
		{"metrics on core", http.MethodGet, core + "/metrics", "", nil, http.StatusOK, ""},
		{"api on core", http.MethodGet, core + "/api/v1/capabilities", "", bearer, http.StatusOK, ""},
	} {
		resp := request(t, tc.method, tc.url, tc.body, tc.header)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: %s %s = %d, want %d", tc.name, tc.method, tc.url, resp.StatusCode, tc.status)
		}
		if got := resp.Header.Get("Location"); got != tc.location {
			t.Errorf("%s: Location = %q, want %q", tc.name, got, tc.location)
		}
	}

	cancel()
	if err := waitServed(t, served); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	for _, addr := range addrs {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = c.Close()
			t.Errorf("%s still accepts connections after shutdown", addr)
		}
	}
}

func TestServe_PanelListenInUseIsStartupError(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cfg := panelServeConfig(t, busy.Addr().String())

	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	served := make(chan error, 1)
	go func() { served <- Serve(context.Background(), cfg, logger, Options{}) }()
	err = waitServed(t, served)
	if err == nil || !strings.Contains(err.Error(), busy.Addr().String()) {
		t.Fatalf("Serve = %v, want an error naming %s", err, busy.Addr())
	}
	if strings.Contains(logs.String(), "listening") {
		t.Errorf("logged listening although startup failed:\n%s", logs)
	}
}
