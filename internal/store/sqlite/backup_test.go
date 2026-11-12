package sqlite

import (
	"context"
	"path/filepath"
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
