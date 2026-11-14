package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// autonomyTables hold a private conversation's status, timers, bar values
// and bar changes.
var autonomyTables = []string{"conversation_status", "conversation_timers", "conversation_buffs", "conversation_buff_events"}

var knownTimerKinds = []store.TimerKind{store.TimerStatusReturn, store.TimerInitiate, store.TimerFollowUp}

const statusCols = `conversation_key, activity, started_at, until_at, task_id, updated_at`

const timerCols = `conversation_key, kind, bot_instance_id, platform_user_id, due_at, anchor_seq, lease_until, created_at`

// SetStatus stores a zero UpdatedAt as now.
func (db *DB) SetStatus(ctx context.Context, s store.ConversationStatus, ret *store.ConversationTimer) error {
	if s.ConversationKey == "" {
		return errors.New("set status: empty conversation key")
	}
	var timer store.ConversationTimer
	if ret != nil {
		timer = *ret
		if timer.ConversationKey == "" {
			timer.ConversationKey = s.ConversationKey
		}
		if timer.Kind == "" {
			timer.Kind = store.TimerStatusReturn
		}
		if timer.ConversationKey != s.ConversationKey || timer.Kind != store.TimerStatusReturn {
			return fmt.Errorf("set status of %s: return timer is %s of %s", s.ConversationKey, timer.Kind, timer.ConversationKey)
		}
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_status (`+statusCols+`) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (conversation_key) DO UPDATE SET activity = excluded.activity, started_at = excluded.started_at,
				until_at = excluded.until_at, task_id = excluded.task_id, updated_at = excluded.updated_at`,
			s.ConversationKey, s.Activity, fmtTime(s.StartedAt), fmtTime(s.UntilAt), s.TaskID,
			fmtTime(nowIfZero(s.UpdatedAt))); err != nil {
			return err
		}
		if ret == nil {
			_, err := deleteTimers(ctx, tx, s.ConversationKey, []store.TimerKind{store.TimerStatusReturn})
			return err
		}
		return armTimer(ctx, tx, timer)
	})
}

func (db *DB) GetStatus(ctx context.Context, convKey string) (store.ConversationStatus, error) {
	var s store.ConversationStatus
	var started, until, updated string
	err := db.r.QueryRowContext(ctx, `SELECT `+statusCols+` FROM conversation_status WHERE conversation_key = ?`, convKey).
		Scan(&s.ConversationKey, &s.Activity, &started, &until, &s.TaskID, &updated)
	if err != nil {
		return store.ConversationStatus{}, notFound(err)
	}
	if s.StartedAt, err = parseTime(started); err != nil {
		return store.ConversationStatus{}, err
	}
	if s.UntilAt, err = parseTime(until); err != nil {
		return store.ConversationStatus{}, err
	}
	if s.UpdatedAt, err = parseTime(updated); err != nil {
		return store.ConversationStatus{}, err
	}
	return s, nil
}

func (db *DB) ClearStatus(ctx context.Context, convKey string, untilAt time.Time) (bool, error) {
	var cleared bool
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		query, args := `DELETE FROM conversation_status WHERE conversation_key = ?`, []any{convKey}
		if !untilAt.IsZero() {
			query += ` AND until_at = ?`
			args = append(args, fmtTime(untilAt))
		}
		n, err := execCount(ctx, tx, query, args...)
		if err != nil {
			return err
		}
		cleared = n > 0
		if !cleared && !untilAt.IsZero() {
			return nil
		}
		_, err = deleteTimers(ctx, tx, convKey, []store.TimerKind{store.TimerStatusReturn})
		return err
	})
	if err != nil {
		return false, err
	}
	return cleared, nil
}

func (db *DB) ArmTimer(ctx context.Context, t store.ConversationTimer) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		return armTimer(ctx, tx, t)
	})
}

// armTimer upserts t by conversation and kind with no lease; a zero
// CreatedAt is stored as now.
func armTimer(ctx context.Context, tx *sql.Tx, t store.ConversationTimer) error {
	if t.ConversationKey == "" {
		return errors.New("arm timer: empty conversation key")
	}
	if !slices.Contains(knownTimerKinds, t.Kind) {
		return fmt.Errorf("arm timer of %s: unknown kind %q", t.ConversationKey, t.Kind)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO conversation_timers (`+timerCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (conversation_key, kind) DO UPDATE SET bot_instance_id = excluded.bot_instance_id,
			platform_user_id = excluded.platform_user_id, due_at = excluded.due_at, anchor_seq = excluded.anchor_seq,
			lease_until = excluded.lease_until, created_at = excluded.created_at`,
		t.ConversationKey, string(t.Kind), t.BotInstanceID, t.PlatformUserID, fmtTime(t.DueAt), t.AnchorSeq,
		fmtTime(time.Time{}), fmtTime(nowIfZero(t.CreatedAt)))
	return err
}

func (db *DB) CancelTimers(ctx context.Context, convKey string, kinds ...store.TimerKind) (int, error) {
	if len(kinds) == 0 {
		return 0, nil
	}
	var n int
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = deleteTimers(ctx, tx, convKey, kinds)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// deleteTimers deletes the conversation's timers of kinds and returns how
// many it deleted.
func deleteTimers(ctx context.Context, tx *sql.Tx, convKey string, kinds []store.TimerKind) (int, error) {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	in, args := inList(names)
	return execCount(ctx, tx, `DELETE FROM conversation_timers WHERE conversation_key = ? AND kind IN (`+in+`)`,
		append([]any{convKey}, args...)...)
}

func (db *DB) ListTimers(ctx context.Context, convKey string) ([]store.ConversationTimer, error) {
	return queryTimers(ctx, db.r, `SELECT `+timerCols+` FROM conversation_timers
		WHERE conversation_key = ? ORDER BY due_at, kind`, convKey)
}

func (db *DB) ClaimDueTimers(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]store.ConversationTimer, error) {
	if limit <= 0 {
		return nil, nil
	}
	nowStr := fmtTime(now)
	until := now.Add(lease).UTC()
	var out []store.ConversationTimer
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = queryTimers(ctx, tx, `SELECT `+timerCols+` FROM conversation_timers
			WHERE due_at <= ? AND lease_until < ? ORDER BY due_at, conversation_key, kind LIMIT ?`, nowStr, nowStr, limit)
		if err != nil {
			return err
		}
		for i := range out {
			if _, err := tx.ExecContext(ctx, `UPDATE conversation_timers SET lease_until = ? WHERE conversation_key = ? AND kind = ?`,
				fmtTime(until), out[i].ConversationKey, string(out[i].Kind)); err != nil {
				return err
			}
			out[i].LeaseUntil = until
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (db *DB) PostponeTimer(ctx context.Context, ref store.TimerRef, dueAt time.Time) (bool, error) {
	n, err := execCount(ctx, db.w, `UPDATE conversation_timers SET due_at = ?, lease_until = ?
		WHERE conversation_key = ? AND kind = ? AND due_at = ?`,
		fmtTime(dueAt), fmtTime(time.Time{}), ref.ConversationKey, string(ref.Kind), fmtTime(ref.DueAt))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// fireTimer deletes the timer ref names; one cancelled, re-armed or
// postponed since gives store.ErrTimerGone.
func fireTimer(ctx context.Context, tx *sql.Tx, ref store.TimerRef) error {
	n, err := execCount(ctx, tx, `DELETE FROM conversation_timers WHERE conversation_key = ? AND kind = ? AND due_at = ?`,
		ref.ConversationKey, string(ref.Kind), fmtTime(ref.DueAt))
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrTimerGone
	}
	return nil
}

// deleteAutonomy deletes the conversation's rows in autonomyTables.
func deleteAutonomy(ctx context.Context, tx *sql.Tx, convKey string) error {
	for _, table := range autonomyTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_key = ?`, convKey); err != nil {
			return err
		}
	}
	return nil
}

type rowsQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryTimers(ctx context.Context, q rowsQueryer, query string, args ...any) ([]store.ConversationTimer, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ConversationTimer
	for rows.Next() {
		var t store.ConversationTimer
		var due, lease, created string
		if err := rows.Scan(&t.ConversationKey, (*string)(&t.Kind), &t.BotInstanceID, &t.PlatformUserID, &due, &t.AnchorSeq,
			&lease, &created); err != nil {
			return nil, err
		}
		if t.DueAt, err = parseTime(due); err != nil {
			return nil, err
		}
		if t.LeaseUntil, err = parseTime(lease); err != nil {
			return nil, err
		}
		if t.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// userMessageScan times the limit is how many of the conversation's newest
// messages UserMessageTimes looks at.
const userMessageScan = 4

// UserMessageTimes counts newest by seq, the order messages were stored in,
// and reads only the newest messages through the (conversation_key, seq)
// index, since occurred_at has no index.
func (db *DB) UserMessageTimes(ctx context.Context, convKey string, since time.Time, limit int) ([]time.Time, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.r.QueryContext(ctx, `SELECT occurred_at FROM (
			SELECT seq, occurred_at, from_bot FROM messages WHERE conversation_key = ? ORDER BY seq DESC LIMIT ?)
		WHERE from_bot = 0 AND occurred_at >= ? ORDER BY seq DESC LIMIT ?`,
		convKey, userMessageScan*limit, fmtTime(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		at, err := parseTime(s)
		if err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}
