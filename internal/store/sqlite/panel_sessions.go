package sqlite

import (
	"context"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func (db *DB) CreatePanelSession(ctx context.Context, s store.PanelSession) error {
	_, err := db.w.ExecContext(ctx, `INSERT INTO panel_sessions
		(token_hash, user, csrf_token, cred_fingerprint, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.TokenHash, s.User, s.CSRFToken, s.CredFingerprint, fmtTime(s.CreatedAt), fmtTime(s.LastSeenAt), fmtTime(s.ExpiresAt))
	return err
}

func (db *DB) GetPanelSession(ctx context.Context, tokenHash string) (store.PanelSession, error) {
	var s store.PanelSession
	var created, lastSeen, expires string
	err := db.r.QueryRowContext(ctx, `SELECT token_hash, user, csrf_token, cred_fingerprint, created_at, last_seen_at, expires_at
		FROM panel_sessions WHERE token_hash = ?`, tokenHash).
		Scan(&s.TokenHash, &s.User, &s.CSRFToken, &s.CredFingerprint, &created, &lastSeen, &expires)
	if err != nil {
		return store.PanelSession{}, notFound(err)
	}
	for _, p := range []struct {
		dst *time.Time
		src string
	}{{&s.CreatedAt, created}, {&s.LastSeenAt, lastSeen}, {&s.ExpiresAt, expires}} {
		if *p.dst, err = parseTime(p.src); err != nil {
			return store.PanelSession{}, err
		}
	}
	return s, nil
}

// TouchPanelSession sets the session's LastSeenAt to at; it returns
// store.ErrNotFound when the session does not exist.
func (db *DB) TouchPanelSession(ctx context.Context, tokenHash string, at time.Time) error {
	return affectedOne(db.w.ExecContext(ctx, `UPDATE panel_sessions SET last_seen_at = ? WHERE token_hash = ?`,
		fmtTime(at), tokenHash))
}

// DeletePanelSession deletes the session; a missing one is not an error.
func (db *DB) DeletePanelSession(ctx context.Context, tokenHash string) error {
	_, err := db.w.ExecContext(ctx, `DELETE FROM panel_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (db *DB) PurgePanelSessions(ctx context.Context, expiredBefore, idleBefore time.Time) (int, error) {
	return execCount(ctx, db.w, `DELETE FROM panel_sessions WHERE expires_at <= ? OR last_seen_at <= ?`,
		fmtTime(expiredBefore), fmtTime(idleBefore))
}
