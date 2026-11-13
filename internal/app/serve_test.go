package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
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

var listeningAddr = regexp.MustCompile(`msg=listening addr=(\S+)`)

func TestServe_ShutdownEndsLongPollPromptly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conloom.yaml")
	yml := strings.ReplaceAll(serveTestConfig, "DATA_DIR", strconv.Quote(filepath.Join(dir, "data")))
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

	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, cfg, logger, Options{}) }()

	var addr string
	for deadline := time.Now().Add(5 * time.Second); addr == ""; {
		if m := listeningAddr.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
			break
		}
		select {
		case err := <-served:
			t.Fatalf("Serve returned early: %v\nlogs:\n%s", err, logs)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start; logs:\n%s", logs)
		}
		time.Sleep(10 * time.Millisecond)
	}

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
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("shutdown took %v with a long poll in flight, want prompt", elapsed)
	}
	r := <-polled
	if r.err != nil || r.status != http.StatusOK || strings.TrimSpace(r.body) != `{"actions":[]}` {
		t.Fatalf("long poll during shutdown: status %d, body %q, err %v; want 200 {\"actions\":[]}", r.status, r.body, r.err)
	}
}
