package mock_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const replConfig = `
storage:
  data_dir: ./data
api_tokens:
  - {name: adapter, token: fake-adapter-token, bot_instance_id: bot_main, scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]}
bots:
  - id: bot_main
    platform: qq
    name: 织语
    platform_self_id: "30001"
    admins: ["29999"]
model_profiles:
  short_context:
    endpoint: MODEL_URL
    model: fake-model
    context: {native_window: 32768, configured_window: 32768, soft_input_budget: 16000, reserved_output_tokens: 1024, safety_margin_tokens: 1024}
    runtime: {max_concurrency: 2, timeout: 10s, max_retries: 0}
    privacy: {allow_sensitive_content: true}
`

func TestREPL_PrintsReply(t *testing.T) {
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return 200, "你好呀" })
	dir := t.TempDir()
	path := filepath.Join(dir, "conloom.yaml")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(replConfig, "MODEL_URL", fake.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	a, err := app.New(cfg, slog.New(slog.DiscardHandler), app.Options{})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)

	m := mock.New(client.New(srv.URL, "fake-adapter-token", nil), "bot_main", "qq")
	conv := mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: "29999"}
	user := mock.User{PlatformID: "29999", Name: "管理员"}
	var out bytes.Buffer
	if err := mock.RunREPL(context.Background(), m, conv, user, strings.NewReader("\n@你好\n"), &out); err != nil {
		t.Fatalf("RunREPL: %v", err)
	}
	if !strings.Contains(out.String(), "织语> 你好呀") {
		t.Errorf("output = %q, want it to contain the reply", out.String())
	}
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1 (the empty line must be ignored)", len(reqs))
	}
}
