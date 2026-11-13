package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store/sqlite"
)

const signalChildEnv = "CONLOOM_TEST_SIGNAL_CHILD"

func TestSecondInterruptForcesExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs POSIX signals")
	}
	if os.Getenv(signalChildEnv) == "1" {
		ctx, stop := signalContext()
		defer stop()
		fmt.Println("ready")
		<-ctx.Done()
		fmt.Println("stopping")
		time.Sleep(30 * time.Second) // a shutdown that takes too long
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSecondInterruptForcesExit$")
	cmd.Env = append(os.Environ(), signalChildEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	lines := bufio.NewScanner(stdout)
	waitLine := func(want string) {
		t.Helper()
		for lines.Scan() {
			if lines.Text() == want {
				return
			}
		}
		_ = cmd.Process.Kill()
		t.Fatalf("child exited before printing %q", want)
	}

	waitLine("ready")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitLine("stopping")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		if code := cmd.ProcessState.ExitCode(); code != -1 {
			t.Fatalf("child exit code %d, want termination by the second interrupt", code)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("second interrupt did not terminate the process")
	}
}

func TestBackup_PrivateFileAndExistingTargetIsUsageError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "conloom.yaml")
	if err := os.WriteFile(cfgPath, []byte("storage:\n  data_dir: ./data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(dir, "data", "conloom.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "backup.db")
	backup := func() (int, string) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, []string{"backup", "-c", cfgPath, "-o", out}, stdio{in: strings.NewReader(""), out: &stdout, err: &stderr})
		return code, stderr.String()
	}
	if code, stderr := backup(); code != 0 {
		t.Fatalf("backup: exit %d, stderr %s", code, stderr)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("backup file mode = %v, want 0600", fi.Mode().Perm())
	}

	code, stderr := backup()
	if code != exitConfig || !strings.Contains(stderr, "already exists") {
		t.Fatalf("backup over existing file: exit %d, stderr %q; want exit %d", code, stderr, exitConfig)
	}
}

func runCommand(ctx context.Context, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(ctx, args, stdio{in: strings.NewReader(""), out: &stdout, err: &stderr})
	return code, stdout.String(), stderr.String()
}

func TestAdapterTelegram_ConfigErrors(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "conloom.yaml")
	if err := os.WriteFile(cfgPath, []byte("adapters:\n  telegram:\n    poll_timeout: 90s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCommand(context.Background(), "adapter", "telegram", "-c", cfgPath)
	if code != exitConfig {
		t.Fatalf("exit %d, want %d; stderr %s", code, exitConfig, stderr)
	}
	for _, w := range []string{"adapters.telegram.token", "adapters.telegram.core_token", "adapters.telegram.poll_timeout"} {
		if !strings.Contains(stderr, w) {
			t.Errorf("stderr %q does not mention %s", stderr, w)
		}
	}

	code, _, stderr = runCommand(context.Background(), "adapter", "nosuch")
	if code != exitConfig || !strings.Contains(stderr, "conloom adapter napcat|telegram") {
		t.Errorf("unknown adapter: exit %d, stderr %q; want a usage error listing telegram", code, stderr)
	}
	code, stdout, _ := runCommand(context.Background(), "help")
	if code != 0 || !strings.Contains(stdout, "conloom adapter telegram -c conloom.yaml") {
		t.Errorf("help: exit %d, stdout %q; want the telegram adapter in the usage", code, stdout)
	}
}

func TestAdapterTelegram_RunsUntilCancelledWithoutLeakingToken(t *testing.T) {
	const token = "30001:AAcmd-test-token_value"
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "conloom.yaml")
	cfg := `storage:
  data_dir: ./data
adapters:
  telegram:
    token: ` + token + `
    api_base: http://127.0.0.1:9
    core_url: http://127.0.0.1:9
    core_token: fake-tg-adapter-token
    bot_instance_id: bot_tg
observability:
  log_format: text
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	code, _, stderr := runCommand(ctx, "adapter", "telegram", "-c", cfgPath)
	if code != 0 {
		t.Fatalf("exit %d, want 0 after cancellation; stderr %s", code, stderr)
	}
	if !strings.Contains(stderr, "starting telegram adapter") || !strings.Contains(stderr, "getMe") {
		t.Errorf("stderr %q, want the start line and a getMe retry", stderr)
	}
	if strings.Contains(stderr, token) || strings.Contains(stderr, "AAcmd-test-token_value") {
		t.Errorf("stderr leaks the bot token: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "telegram-dedup.log")); err != nil {
		t.Errorf("dedup log not created in the default state_dir: %v", err)
	}
}

func TestAdapterTelegram_RejectedTokenExitsWithConfigCode(t *testing.T) {
	const token = "30001:AArejected-token_value"
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	}))
	t.Cleanup(tg.Close)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "conloom.yaml")
	cfg := `storage:
  data_dir: ./data
adapters:
  telegram:
    token: ` + token + `
    api_base: ` + tg.URL + `
    core_url: http://127.0.0.1:9
    core_token: fake-tg-adapter-token
    bot_instance_id: bot_tg
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, _, stderr := runCommand(ctx, "adapter", "telegram", "-c", cfgPath)
	if code != exitConfig || ctx.Err() != nil {
		t.Fatalf("exit %d, want %d before the deadline; stderr %s", code, exitConfig, stderr)
	}
	if !strings.Contains(stderr, "rejected") || strings.Contains(stderr, "AArejected-token_value") {
		t.Errorf("stderr = %q, want the rejection reported without the token", stderr)
	}
}

// TestSystemdUnits_DoNotRestartOnConfigErrors checks that every unit keeps
// systemd from restarting a process that exited with exitConfig, which a
// restart cannot fix.
func TestSystemdUnits_DoNotRestartOnConfigErrors(t *testing.T) {
	units, err := filepath.Glob("../../deploy/systemd/*.service")
	if err != nil || len(units) != 3 {
		t.Fatalf("units = %v (err %v), want the core, napcat and telegram units", units, err)
	}
	want := fmt.Sprintf("RestartPreventExitStatus=%d", exitConfig)
	for _, u := range units {
		data, err := os.ReadFile(u)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "\n"+want+"\n") {
			t.Errorf("%s lacks %s", filepath.Base(u), want)
		}
	}
}
