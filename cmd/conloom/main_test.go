package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
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
