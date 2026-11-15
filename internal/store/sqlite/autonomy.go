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

// autonomyTables hold a private conversation's status, timers, bar values,
// bar changes, bot-created bars and effects; a shared panel memory scope
// holds all but timers, which stay with each of its chats.
var autonomyTables = []string{"conversation_status", "conversation_timers", "conversation_buffs", "conversation_buff_events",
	"conversation_custom_bars", "conversation_effects"}

var knownTimerKinds = []store.TimerKind{store.TimerStatusReturn, store.TimerInitiate, store.TimerFollowUp}

const statusCols = `conversation_key, activity, started_at, until_at, task_id, updated_at, origin_conversation_key`

const timerCols = `conversation_key, kind, bot_instance_id, platform_user_id, due_at, anchor_seq, lease_until, created_at`

const buffCols = `conversation_key, name, value, day, day_delta, updated_at`

const buffEventCols = `conversation_key, name, task_id, source, before, after, reason, created_at, kind`

const customBarCols = `conversation_key, name, baseline, half_life_seconds, description, task_id, created_at`

const effectCols = `conversation_key, name, description, started_at, until_at, task_id`

// buffTaskChangesQuery counts the changes of a kind a task (a non-empty
// task_id) made of a name, through the partial unique index on (task_id,
// name, kind), which allows it one.
const buffTaskChangesQuery = `SELECT COUNT(*) FROM conversation_buff_events WHERE task_id = ? AND name = ? AND kind = ? AND task_id <> ''`

const (
	customBarTaskQuery    = `SELECT task_id FROM conversation_custom_bars WHERE conversation_key = ? AND name = ?`
	effectTaskQuery       = `SELECT task_id FROM conversation_effects WHERE conversation_key = ? AND name = ?`
	activeEffectTaskQuery = effectTaskQuery + ` AND until_at > ?`
)

// SetStatus stores a zero UpdatedAt as now.
func (db *DB) SetStatus(ctx context.Context, s store.ConversationStatus, ret *store.ConversationTimer) error {
	if s.ConversationKey == "" {
		return errors.New("set status: empty conversation key")
	}
	origin := s.Origin()
	var timer store.ConversationTimer
	if ret != nil {
		timer = *ret
		if timer.ConversationKey == "" {
			timer.ConversationKey = origin
		}
		if timer.Kind == "" {
			timer.Kind = store.TimerStatusReturn
		}
		if timer.ConversationKey != origin || timer.Kind != store.TimerStatusReturn {
			return fmt.Errorf("set status of %s in %s: return timer is %s of %s", s.ConversationKey, origin, timer.Kind,
				timer.ConversationKey)
		}
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		prev, found, err := statusOrigin(ctx, tx, s.ConversationKey)
		if err != nil {
			return err
		}
		if found && prev != origin {
			if _, err := deleteTimers(ctx, tx, prev, []store.TimerKind{store.TimerStatusReturn}); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_status (`+statusCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (conversation_key) DO UPDATE SET activity = excluded.activity, started_at = excluded.started_at,
				until_at = excluded.until_at, task_id = excluded.task_id, updated_at = excluded.updated_at,
				origin_conversation_key = excluded.origin_conversation_key`,
			s.ConversationKey, s.Activity, fmtTime(s.StartedAt), fmtTime(s.UntilAt), s.TaskID,
			fmtTime(nowIfZero(s.UpdatedAt)), s.OriginConversationKey); err != nil {
			return err
		}
		if ret == nil {
			_, err := deleteTimers(ctx, tx, origin, []store.TimerKind{store.TimerStatusReturn})
			return err
		}
		return armTimer(ctx, tx, timer)
	})
}

// statusOrigin returns the chat the status of convKey was set in
// (store.ConversationStatus.Origin), or convKey and false without a status.
func statusOrigin(ctx context.Context, q queryer, convKey string) (string, bool, error) {
	var origin string
	err := q.QueryRowContext(ctx, `SELECT origin_conversation_key FROM conversation_status WHERE conversation_key = ?`,
		convKey).Scan(&origin)
	if errors.Is(err, sql.ErrNoRows) {
		return convKey, false, nil
	}
	if err != nil {
		return "", false, err
	}
	return store.ConversationStatus{ConversationKey: convKey, OriginConversationKey: origin}.Origin(), true, nil
}

func (db *DB) GetStatus(ctx context.Context, convKey string) (store.ConversationStatus, error) {
	var s store.ConversationStatus
	var started, until, updated string
	err := db.r.QueryRowContext(ctx, `SELECT `+statusCols+` FROM conversation_status WHERE conversation_key = ?`, convKey).
		Scan(&s.ConversationKey, &s.Activity, &started, &until, &s.TaskID, &updated, &s.OriginConversationKey)
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
		origin, _, err := statusOrigin(ctx, tx, convKey)
		if err != nil {
			return err
		}
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
		_, err = deleteTimers(ctx, tx, origin, []store.TimerKind{store.TimerStatusReturn})
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

func (db *DB) DeleteTimer(ctx context.Context, ref store.TimerRef) (bool, error) {
	n, err := deleteTimer(ctx, db.w, ref)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// fireTimer deletes the timer ref names; one cancelled, re-armed or
// postponed since gives store.ErrTimerGone.
func fireTimer(ctx context.Context, tx *sql.Tx, ref store.TimerRef) error {
	n, err := deleteTimer(ctx, tx, ref)
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrTimerGone
	}
	return nil
}

// deleteTimer deletes the timer ref names, if it is still armed for
// ref.DueAt, and returns how many rows it deleted.
func deleteTimer(ctx context.Context, e execer, ref store.TimerRef) (int, error) {
	return execCount(ctx, e, `DELETE FROM conversation_timers WHERE conversation_key = ? AND kind = ? AND due_at = ?`,
		ref.ConversationKey, string(ref.Kind), fmtTime(ref.DueAt))
}

func (db *DB) GetBuffs(ctx context.Context, convKey string) ([]store.BuffValue, error) {
	return queryBuffs(ctx, db.r, `SELECT `+buffCols+` FROM conversation_buffs WHERE conversation_key = ? ORDER BY name`, convKey)
}

func (db *DB) UpdateBuff(ctx context.Context, convKey, name string, ev store.BuffEvent,
	apply func(cur store.BuffValue, found bool) (store.BuffValue, float64, error)) (store.BuffValue, error) {
	if convKey == "" || name == "" {
		return store.BuffValue{}, errors.New("update bar: empty conversation key or name")
	}
	var next store.BuffValue
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if done, err := taskChanged(ctx, tx, ev.TaskID, name, store.BuffKindAdjust); err != nil {
			return err
		} else if done {
			return store.ErrBuffDuplicate
		}
		stored, err := queryBuffs(ctx, tx, `SELECT `+buffCols+` FROM conversation_buffs WHERE conversation_key = ? AND name = ?`,
			convKey, name)
		if err != nil {
			return err
		}
		var cur store.BuffValue
		found := len(stored) > 0
		if found {
			cur = stored[0]
		}
		var before float64
		if next, before, err = apply(cur, found); err != nil {
			return err
		}
		next.ConversationKey, next.Name, next.UpdatedAt = convKey, name, nowIfZero(next.UpdatedAt)
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_buffs (`+buffCols+`) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (conversation_key, name) DO UPDATE SET value = excluded.value, day = excluded.day,
				day_delta = excluded.day_delta, updated_at = excluded.updated_at`,
			convKey, name, next.Value, next.Day, next.DayDelta, fmtTime(next.UpdatedAt)); err != nil {
			return err
		}
		ev.ConversationKey, ev.Name, ev.Kind, ev.Before, ev.After = convKey, name, store.BuffKindAdjust, before, next.Value
		return insertBuffEvent(ctx, tx, ev)
	})
	if err != nil {
		return store.BuffValue{}, err
	}
	return next, nil
}

func (db *DB) ListBuffEvents(ctx context.Context, convKey string, limit int) ([]store.BuffEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.r.QueryContext(ctx, `SELECT id, `+buffEventCols+` FROM conversation_buff_events
		WHERE conversation_key = ? ORDER BY created_at DESC, id DESC LIMIT ?`, convKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.BuffEvent
	for rows.Next() {
		var e store.BuffEvent
		var created string
		if err := rows.Scan(&e.ID, &e.ConversationKey, &e.Name, &e.TaskID, &e.Source, &e.Before, &e.After, &e.Reason,
			&created, &e.Kind); err != nil {
			return nil, err
		}
		if e.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (db *DB) PurgeBuffEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := db.w.ExecContext(ctx, `DELETE FROM conversation_buff_events WHERE created_at < ?`, fmtTime(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (db *DB) AddCustomBar(ctx context.Context, b store.CustomBar, initial float64, limit int) error {
	if b.ConversationKey == "" || b.Name == "" {
		return errors.New("add bar: empty conversation key or name")
	}
	b.CreatedAt = nowIfZero(b.CreatedAt)
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if done, err := taskChanged(ctx, tx, b.TaskID, b.Name, store.BuffKindBarAdd); err != nil || done {
			return err
		}
		holder, found, err := queryString(ctx, tx, customBarTaskQuery, b.ConversationKey, b.Name)
		switch {
		case err != nil:
			return err
		case found && b.TaskID != "" && holder == b.TaskID:
			return nil
		case found:
			return store.ErrBuffNameTaken
		}
		if _, found, err := queryString(ctx, tx, activeEffectTaskQuery, b.ConversationKey, b.Name, fmtTime(b.CreatedAt)); err != nil {
			return err
		} else if found {
			return store.ErrBuffNameTaken
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_custom_bars WHERE conversation_key = ?`,
			b.ConversationKey).Scan(&n); err != nil {
			return err
		}
		if n >= limit {
			return store.ErrBuffLimit
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_custom_bars (`+customBarCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			b.ConversationKey, b.Name, b.Baseline, int64(b.HalfLife/time.Second), b.Description, b.TaskID,
			fmtTime(b.CreatedAt)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_buffs (`+buffCols+`) VALUES (?, ?, ?, '', 0, ?)
			ON CONFLICT (conversation_key, name) DO UPDATE SET value = excluded.value, day = excluded.day,
				day_delta = excluded.day_delta, updated_at = excluded.updated_at`,
			b.ConversationKey, b.Name, initial, fmtTime(b.CreatedAt)); err != nil {
			return err
		}
		return insertBuffEvent(ctx, tx, store.BuffEvent{ConversationKey: b.ConversationKey, Name: b.Name, Kind: store.BuffKindBarAdd,
			TaskID: b.TaskID, Source: store.BuffSourceModel, Before: initial, After: initial, CreatedAt: b.CreatedAt})
	})
}

func (db *DB) RemoveCustomBar(ctx context.Context, convKey, name, source, taskID string, now time.Time) (bool, error) {
	var removed bool
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		ok, err := removable(ctx, tx, customBarTaskQuery, convKey, name, taskID, store.BuffKindBarRemove)
		if err != nil || !ok {
			return err
		}
		var value float64
		err = tx.QueryRowContext(ctx, `SELECT value FROM conversation_buffs WHERE conversation_key = ? AND name = ?`,
			convKey, name).Scan(&value)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, table := range []string{"conversation_custom_bars", "conversation_buffs"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_key = ? AND name = ?`, convKey, name); err != nil {
				return err
			}
		}
		removed = true
		return insertBuffEvent(ctx, tx, store.BuffEvent{ConversationKey: convKey, Name: name, Kind: store.BuffKindBarRemove,
			TaskID: taskID, Source: source, Before: value, After: value, CreatedAt: now})
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

func (db *DB) ListCustomBars(ctx context.Context, convKey string) ([]store.CustomBar, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+customBarCols+` FROM conversation_custom_bars
		WHERE conversation_key = ? ORDER BY created_at, rowid`, convKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.CustomBar
	for rows.Next() {
		var b store.CustomBar
		var halfLife int64
		var created string
		if err := rows.Scan(&b.ConversationKey, &b.Name, &b.Baseline, &halfLife, &b.Description, &b.TaskID, &created); err != nil {
			return nil, err
		}
		b.HalfLife = time.Duration(halfLife) * time.Second
		if b.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (db *DB) AddEffect(ctx context.Context, e store.Effect, limit int, now time.Time) error {
	if e.ConversationKey == "" || e.Name == "" {
		return errors.New("add effect: empty conversation key or name")
	}
	if e.StartedAt.IsZero() {
		e.StartedAt = now
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if done, err := taskChanged(ctx, tx, e.TaskID, e.Name, store.BuffKindEffectAdd); err != nil || done {
			return err
		}
		holder, found, err := queryString(ctx, tx, activeEffectTaskQuery, e.ConversationKey, e.Name, fmtTime(now))
		switch {
		case err != nil:
			return err
		case found && e.TaskID != "" && holder == e.TaskID:
			return nil
		case found:
			return store.ErrBuffNameTaken
		}
		if _, found, err := queryString(ctx, tx, customBarTaskQuery, e.ConversationKey, e.Name); err != nil {
			return err
		} else if found {
			return store.ErrBuffNameTaken
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_effects WHERE conversation_key = ? AND until_at > ?`,
			e.ConversationKey, fmtTime(now)).Scan(&n); err != nil {
			return err
		}
		if n >= limit {
			return store.ErrBuffLimit
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_effects (`+effectCols+`) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (conversation_key, name) DO UPDATE SET description = excluded.description,
				started_at = excluded.started_at, until_at = excluded.until_at, task_id = excluded.task_id`,
			e.ConversationKey, e.Name, e.Description, fmtTime(e.StartedAt), fmtTime(e.UntilAt), e.TaskID); err != nil {
			return err
		}
		return insertBuffEvent(ctx, tx, store.BuffEvent{ConversationKey: e.ConversationKey, Name: e.Name,
			Kind: store.BuffKindEffectAdd, TaskID: e.TaskID, Source: store.BuffSourceModel, CreatedAt: now})
	})
}

func (db *DB) RemoveEffect(ctx context.Context, convKey, name, source, taskID string, now time.Time) (bool, error) {
	var removed bool
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		ok, err := removable(ctx, tx, effectTaskQuery, convKey, name, taskID, store.BuffKindEffectRemove)
		if err != nil || !ok {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_effects WHERE conversation_key = ? AND name = ?`,
			convKey, name); err != nil {
			return err
		}
		removed = true
		return insertBuffEvent(ctx, tx, store.BuffEvent{ConversationKey: convKey, Name: name, Kind: store.BuffKindEffectRemove,
			TaskID: taskID, Source: source, CreatedAt: now})
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

func (db *DB) ListEffects(ctx context.Context, convKey string, now time.Time) ([]store.Effect, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+effectCols+` FROM conversation_effects
		WHERE conversation_key = ? AND until_at > ? ORDER BY started_at, rowid`, convKey, fmtTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Effect
	for rows.Next() {
		var e store.Effect
		var started, until string
		if err := rows.Scan(&e.ConversationKey, &e.Name, &e.Description, &started, &until, &e.TaskID); err != nil {
			return nil, err
		}
		if e.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if e.UntilAt, err = parseTime(until); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (db *DB) PurgeExpiredEffects(ctx context.Context, now time.Time) (int, error) {
	return execCount(ctx, db.w, `DELETE FROM conversation_effects WHERE until_at <= ?`, fmtTime(now))
}

// insertBuffEvent records ev under its ConversationKey and Name, a zero
// CreatedAt as now.
func insertBuffEvent(ctx context.Context, tx *sql.Tx, ev store.BuffEvent) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO conversation_buff_events (`+buffEventCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ConversationKey, ev.Name, ev.TaskID, ev.Source, ev.Before, ev.After, ev.Reason, fmtTime(nowIfZero(ev.CreatedAt)), ev.Kind)
	return err
}

// taskChanged reports whether task, when set, already made a change of kind
// of name.
func taskChanged(ctx context.Context, q queryer, task, name, kind string) (bool, error) {
	if task == "" {
		return false, nil
	}
	var n int
	if err := q.QueryRowContext(ctx, buffTaskChangesQuery, task, name, kind).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// removable reports whether the conversation has the bot-created bar or
// effect name that query (customBarTaskQuery or effectTaskQuery) looks up,
// and returns store.ErrBuffDuplicate when it has, but taskID already made a
// change of kind (its removal) of that name.
func removable(ctx context.Context, tx *sql.Tx, query, convKey, name, taskID, kind string) (bool, error) {
	if _, found, err := queryString(ctx, tx, query, convKey, name); err != nil || !found {
		return false, err
	}
	if done, err := taskChanged(ctx, tx, taskID, name, kind); err != nil {
		return false, err
	} else if done {
		return false, store.ErrBuffDuplicate
	}
	return true, nil
}

// queryString returns the single text column of the row query finds, and
// whether it finds one.
func queryString(ctx context.Context, q queryer, query string, args ...any) (string, bool, error) {
	var s string
	err := q.QueryRowContext(ctx, query, args...).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return s, true, nil
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

func queryBuffs(ctx context.Context, q rowsQueryer, query string, args ...any) ([]store.BuffValue, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.BuffValue
	for rows.Next() {
		var b store.BuffValue
		var updated string
		if err := rows.Scan(&b.ConversationKey, &b.Name, &b.Value, &b.Day, &b.DayDelta, &updated); err != nil {
			return nil, err
		}
		if b.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out = append(out, b)
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
