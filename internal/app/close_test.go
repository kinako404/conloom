package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
)

// stuckSink holds every write until release is closed.
type stuckSink struct{ release chan struct{} }

func (s *stuckSink) InsertModelCalls(context.Context, []store.ModelCall) error {
	<-s.release
	return nil
}

func TestClose_UsageFlushTimeoutIsOnlyLogged(t *testing.T) {
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
	logs := &lockedBuffer{}
	a, err := New(cfg, slog.New(slog.NewTextHandler(logs, nil)), Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.usage.Close(context.Background()); err != nil {
		t.Fatalf("close the app's own recorder: %v", err)
	}
	sink := &stuckSink{release: make(chan struct{})}
	defer close(sink.release)
	a.usage = usage.NewRecorder(sink, usage.RecorderOptions{})
	a.usage.Record(store.ModelCall{Purpose: store.PurposeChat})
	a.usageFlushTimeout = 50 * time.Millisecond

	start := time.Now()
	if err := a.Close(); err != nil {
		t.Errorf("Close = %v, want nil when only the usage flush timed out", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Close took %v, want about the 50ms flush timeout", elapsed)
	}
	if got := logs.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "model calls") {
		t.Errorf("logs = %q, want a warning about unwritten model calls", got)
	}
}
