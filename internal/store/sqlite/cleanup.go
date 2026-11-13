package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func (db *DB) PurgeFinishedTasks(ctx context.Context, before time.Time, limit int) (int, int, error) {
	if limit <= 0 {
		return 0, 0, nil
	}
	var tasks, actions int
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		ids, err := queryStrings(ctx, tx, `SELECT id FROM tasks
			WHERE status IN (?, ?, ?) AND updated_at < ?
			AND NOT EXISTS (SELECT 1 FROM outbox_actions o WHERE o.task_id = tasks.id AND o.status IN (?, ?))
			LIMIT ?`,
			conloomapi.TaskSucceeded, conloomapi.TaskFailed, conloomapi.TaskCancelled, fmtTime(before),
			store.OutboxPending, store.OutboxLeased, limit)
		if err != nil || len(ids) == 0 {
			return err
		}
		in, args := inList(ids)
		if actions, err = execCount(ctx, tx, `DELETE FROM outbox_actions WHERE task_id IN (`+in+`)`, args...); err != nil {
			return err
		}
		tasks, err = execCount(ctx, tx, `DELETE FROM tasks WHERE id IN (`+in+`)`, args...)
		return err
	})
	if err != nil {
		return 0, 0, err
	}
	return tasks, actions, nil
}

func (db *DB) PurgeOutbox(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	return execCount(ctx, db.w, `DELETE FROM outbox_actions WHERE seq IN (
		SELECT seq FROM outbox_actions o
		WHERE o.status IN (?, ?) AND o.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id = o.task_id)
		LIMIT ?)`,
		store.OutboxAcked, store.OutboxExpired, fmtTime(before), limit)
}

func (db *DB) PurgeAudit(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	return execCount(ctx, db.w, `DELETE FROM audit_events WHERE rowid IN (
		SELECT rowid FROM audit_events WHERE created_at < ? LIMIT ?)`, fmtTime(before), limit)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// execCount runs a statement and returns the number of rows it affected.
func execCount(ctx context.Context, e execer, query string, args ...any) (int, error) {
	res, err := e.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func queryStrings(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// inList returns "?, ?, …" for values and values as arguments.
func inList(values []string) (string, []any) {
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return strings.TrimSuffix(strings.Repeat("?, ", len(values)), ", "), args
}
