// Package sqlite implements store.Store on SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strconv"
	"strings"
	"time"

	sqlitedrv "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/kinako404/conloom/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"

// timeLayout is RFC3339Nano with a fixed-width fraction so stored UTC times sort lexically.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

var _ store.Store = (*DB)(nil)

// DB serialises writes through a single connection and serves reads from a pool.
type DB struct {
	w *sql.DB
	r *sql.DB
}

func Open(ctx context.Context, path string) (*DB, error) {
	return open(ctx, path, migrations)
}

// open opens path and applies the migrations in fsys's migrations
// directory that are not applied yet.
func open(ctx context.Context, path string, fsys fs.FS) (*DB, error) {
	w, err := sql.Open("sqlite", path+"?_txlock=immediate&"+pragmas)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", path+"?"+pragmas)
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(runtime.NumCPU())
	db := &DB{w: w, r: r}
	if err := db.migrate(ctx, fsys); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error {
	return errors.Join(db.r.Close(), db.w.Close())
}

func (db *DB) migrate(ctx context.Context, fsys fs.FS) error {
	if _, err := db.w.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		num, _, _ := strings.Cut(name, "_")
		version, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("migration %s: bad version prefix", name)
		}
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return err
		}
		if err := db.applyMigration(ctx, version, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

func (db *DB) applyMigration(ctx context.Context, version int, body string) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, body); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, fmtTime(time.Now()))
		return err
	})
}

// inTx runs fn in a write transaction, committing on nil and rolling back otherwise.
func (db *DB) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (db *DB) UpsertBotInstance(ctx context.Context, id, platform, name string) error {
	now := fmtTime(time.Now())
	_, err := db.w.ExecContext(ctx, `
		INSERT INTO bot_instances (id, platform, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET platform = excluded.platform, name = excluded.name, updated_at = excluded.updated_at`,
		id, platform, name, now, now)
	return err
}

func fmtTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// nowIfZero returns t, or the current time when t is zero.
func nowIfZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func isUniqueViolation(err error) bool {
	var e *sqlitedrv.Error
	return errors.As(err, &e) && e.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// notFound maps sql.ErrNoRows to store.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}
