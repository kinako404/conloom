package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const taskCols = `id, bot_instance_id, conversation_key, status, actor_platform_user_id, command,
	trigger_message_ids, priority, meta, error_code, event_occurred_at, created_at, updated_at`

func insertTask(ctx context.Context, tx *sql.Tx, t store.TaskRecord) error {
	triggers, err := json.Marshal(t.TriggerMessageIDs)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(t.Meta)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks (`+taskCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.BotInstanceID, t.ConversationKey, t.Status, t.ActorPlatformUserID, t.Command,
		string(triggers), t.Priority, string(meta), t.ErrorCode, fmtTime(t.EventOccurredAt),
		fmtTime(nowIfZero(t.CreatedAt)), fmtTime(nowIfZero(t.UpdatedAt)))
	return err
}

func appendTrigger(ctx context.Context, tx *sql.Tx, botID, taskID, messageID string) error {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT trigger_message_ids FROM tasks WHERE bot_instance_id = ? AND id = ?`,
		botID, taskID).Scan(&raw)
	if err != nil {
		return notFound(err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return err
	}
	updated, err := json.Marshal(append(ids, messageID))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET trigger_message_ids = ?, updated_at = ? WHERE id = ?`,
		string(updated), fmtTime(time.Now()), taskID)
	return err
}

// cancelUnfinishedTask stores the queued or running task as cancelled; a
// finished one gives store.ErrTaskFinished.
func cancelUnfinishedTask(ctx context.Context, tx *sql.Tx, botID, taskID string) error {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE bot_instance_id = ? AND id = ?`,
		botID, taskID).Scan(&status)
	if err != nil {
		return notFound(err)
	}
	if status != conloomapi.TaskQueued && status != conloomapi.TaskRunning {
		return store.ErrTaskFinished
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET status = ?, error_code = ?, updated_at = ? WHERE id = ?`,
		conloomapi.TaskCancelled, conloomapi.CodeTaskCancelled, fmtTime(time.Now()), taskID)
	return err
}

func scanTask(row scanner) (store.TaskRecord, error) {
	var t store.TaskRecord
	var triggers, meta, occurred, created, updated string
	err := row.Scan(&t.ID, &t.BotInstanceID, &t.ConversationKey, &t.Status, &t.ActorPlatformUserID, &t.Command,
		&triggers, &t.Priority, &meta, &t.ErrorCode, &occurred, &created, &updated)
	if err != nil {
		return t, notFound(err)
	}
	if err := json.Unmarshal([]byte(triggers), &t.TriggerMessageIDs); err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(meta), &t.Meta); err != nil {
		return t, err
	}
	if t.EventOccurredAt, err = parseTime(occurred); err != nil {
		return t, err
	}
	if t.CreatedAt, err = parseTime(created); err != nil {
		return t, err
	}
	t.UpdatedAt, err = parseTime(updated)
	return t, err
}

func (db *DB) GetTask(ctx context.Context, botID, taskID string) (store.TaskRecord, error) {
	return scanTask(db.r.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE bot_instance_id = ? AND id = ?`,
		botID, taskID))
}

func (db *DB) SetTaskStatus(ctx context.Context, taskID, status string) error {
	res, err := db.w.ExecContext(ctx, `UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`,
		status, fmtTime(time.Now()), taskID)
	return affectedOne(res, err)
}

func (db *DB) CompleteTask(ctx context.Context, c store.TaskCompletion) error {
	meta, err := json.Marshal(c.Meta)
	if err != nil {
		return err
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, meta = ?, error_code = ?, updated_at = ? WHERE id = ?`,
			c.Status, string(meta), c.ErrorCode, fmtTime(time.Now()), c.TaskID)
		if err := affectedOne(res, err); err != nil {
			return err
		}
		for _, a := range c.Actions {
			if a.TaskID == "" {
				a.TaskID = c.TaskID
			}
			if err := insertOutbox(ctx, tx, a); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertOutbox(ctx context.Context, tx *sql.Tx, o store.OutboxRecord) error {
	payload, err := json.Marshal(o.Action)
	if err != nil {
		return err
	}
	if o.Status == "" {
		o.Status = store.OutboxPending
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO outbox_actions (action_id, bot_instance_id, conversation_key, task_id, platform, status, payload,
			lease_until, attempts, platform_message_id, created_at, expires_at, acked_at, not_before)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		o.Action.ActionID, o.BotInstanceID, o.ConversationKey, o.TaskID, o.Platform, o.Status, string(payload),
		fmtTime(o.LeaseUntil), o.Attempts, o.PlatformMessageID, fmtTime(nowIfZero(o.CreatedAt)), fmtTime(o.ExpiresAt),
		fmtTime(o.NotBefore))
	return err
}

func (db *DB) ListUnfinishedTasks(ctx context.Context) ([]store.TaskRecord, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE status IN (?, ?) ORDER BY created_at, id`,
		conloomapi.TaskQueued, conloomapi.TaskRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.TaskRecord
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (db *DB) ActionsForTask(ctx context.Context, taskID string) ([]conloomapi.Action, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT payload FROM outbox_actions WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []conloomapi.Action
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var a conloomapi.Action
		if err := json.Unmarshal([]byte(payload), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// affectedOne returns store.ErrNotFound when an update matched no rows.
func affectedOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}
