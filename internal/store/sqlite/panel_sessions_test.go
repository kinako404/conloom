package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func panelSession(hash string, created, lastSeen, expires time.Time) store.PanelSession {
	return store.PanelSession{TokenHash: hash, User: "admin", CSRFToken: "csrf-" + hash,
		CredFingerprint: "0123456789abcdef", CreatedAt: created, LastSeenAt: lastSeen, ExpiresAt: expires}
}

func TestPanelSessions_Lifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 8, 0, 0, 123000000, time.UTC)
	s := panelSession("hash-live", now, now, now.Add(168*time.Hour))
	if err := db.CreatePanelSession(ctx, s); err != nil {
		t.Fatalf("CreatePanelSession: %v", err)
	}
	got, err := db.GetPanelSession(ctx, s.TokenHash)
	if err != nil || got != s {
		t.Fatalf("GetPanelSession = %+v, %v; want %+v", got, err, s)
	}
	if _, err := db.GetPanelSession(ctx, "hash-missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPanelSession(missing) err = %v, want ErrNotFound", err)
	}

	seen := now.Add(5 * time.Minute)
	if err := db.TouchPanelSession(ctx, s.TokenHash, seen); err != nil {
		t.Fatalf("TouchPanelSession: %v", err)
	}
	if got, err := db.GetPanelSession(ctx, s.TokenHash); err != nil || !got.LastSeenAt.Equal(seen) || !got.CreatedAt.Equal(now) {
		t.Fatalf("after touch = %+v, %v; want LastSeenAt %v", got, err, seen)
	}
	if err := db.TouchPanelSession(ctx, "hash-missing", seen); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TouchPanelSession(missing) err = %v, want ErrNotFound", err)
	}

	if err := db.DeletePanelSession(ctx, s.TokenHash); err != nil {
		t.Fatalf("DeletePanelSession: %v", err)
	}
	if _, err := db.GetPanelSession(ctx, s.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPanelSession after delete err = %v, want ErrNotFound", err)
	}
	if err := db.DeletePanelSession(ctx, s.TokenHash); err != nil {
		t.Fatalf("DeletePanelSession(missing) = %v, want nil", err)
	}

	for _, s := range []store.PanelSession{
		panelSession("hash-expired", now.Add(-200*time.Hour), now.Add(-time.Hour), now.Add(-time.Minute)),
		panelSession("hash-idle", now.Add(-20*time.Hour), now.Add(-13*time.Hour), now.Add(100*time.Hour)),
		panelSession("hash-valid", now.Add(-20*time.Hour), now.Add(-time.Hour), now.Add(100*time.Hour)),
	} {
		if err := db.CreatePanelSession(ctx, s); err != nil {
			t.Fatalf("CreatePanelSession %s: %v", s.TokenHash, err)
		}
	}
	if n, err := db.PurgePanelSessions(ctx, now, now.Add(-12*time.Hour)); err != nil || n != 2 {
		t.Fatalf("PurgePanelSessions = %d, %v; want 2", n, err)
	}
	for hash, want := range map[string]bool{"hash-expired": false, "hash-idle": false, "hash-valid": true} {
		_, err := db.GetPanelSession(ctx, hash)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetPanelSession %s: %v", hash, err)
		}
		if (err == nil) != want {
			t.Errorf("session %s kept = %v, want %v", hash, err == nil, want)
		}
	}
}
