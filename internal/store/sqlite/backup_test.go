package sqlite

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBackup_ProducesOpenableCopy(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Ingest(ctx, testIngest("k1", "m1", "p1")); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Backup(ctx, path); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	cp, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open backup: %v", err)
	}
	defer cp.Close()
	msg, err := cp.GetMessage(ctx, testBot, "m1")
	if err != nil || msg.Text != "hello m1" {
		t.Fatalf("backup message = %+v, %v", msg, err)
	}

	if err := db.Backup(ctx, path); err == nil {
		t.Fatal("Backup over existing file succeeded")
	}
}

func TestBackup_CreatesPrivateFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX file modes")
	}
	db := newTestDB(t)
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Backup(context.Background(), path); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("backup file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestBackup_FailureLeavesNoFile(t *testing.T) {
	db := newTestDB(t)
	path := filepath.Join(t.TempDir(), "backup.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := db.Backup(ctx, path); err == nil {
		t.Fatal("Backup with a cancelled context succeeded")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after a failed backup, Stat = %v; want no file", err)
	}
	if err := db.Backup(context.Background(), path); err != nil {
		t.Errorf("Backup after a failed one: %v", err)
	}
}
