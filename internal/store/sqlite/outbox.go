package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const outboxCols = `seq, bot_instance_id, conversation_key, task_id, platform, status, payload,
	lease_until, attempts, platform_message_id, created_at, expires_at`

func scanOutbox(row scanner) (store.OutboxRecord, error) {
	var o store.OutboxRecord
	var payload, leaseUntil, created, expires string
	err := row.Scan(&o.Seq, &o.BotInstanceID, &o.ConversationKey, &o.TaskID, &o.Platform, &o.Status, &payload,
		&leaseUntil, &o.Attempts, &o.PlatformMessageID, &created, &expires)
	if err != nil {
		return o, notFound(err)
	}
	if err := json.Unmarshal([]byte(payload), &o.Action); err != nil {
		return o, err
	}
	if o.LeaseUntil, err = parseTime(leaseUntil); err != nil {
		return o, err
	}
	if o.CreatedAt, err = parseTime(created); err != nil {
		return o, err
	}
	o.ExpiresAt, err = parseTime(expires)
	return o, err
}

// LeaseOutbox hands out, per conversation, the oldest live action if it is
// pending or its lease has run out, and marks the handed-out actions leased.
func (db *DB) LeaseOutbox(ctx context.Context, botID string, limit int, now time.Time, lease time.Duration) ([]store.OutboxRecord, error) {
	nowStr := fmtTime(now)
	until := now.Add(lease).UTC()
	var out []store.OutboxRecord
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = queryOutbox(ctx, tx, `SELECT `+outboxCols+` FROM outbox_actions
			WHERE seq IN (
				SELECT MIN(seq) FROM outbox_actions
				WHERE bot_instance_id = ? AND status IN (?, ?) AND expires_at > ?
				GROUP BY conversation_key)
			AND (status = ? OR lease_until < ?)
			ORDER BY seq LIMIT ?`,
			botID, store.OutboxPending, store.OutboxLeased, nowStr, store.OutboxPending, nowStr, limit)
		if err != nil {
			return err
		}
		for i := range out {
			if _, err := tx.ExecContext(ctx, `UPDATE outbox_actions SET status = ?, lease_until = ?, attempts = attempts + 1
				WHERE seq = ?`, store.OutboxLeased, fmtTime(until), out[i].Seq); err != nil {
				return err
			}
			out[i].Status = store.OutboxLeased
			out[i].LeaseUntil = until
			out[i].Attempts++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func queryOutbox(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]store.OutboxRecord, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.OutboxRecord
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (db *DB) GetOutbox(ctx context.Context, botID, actionID string) (store.OutboxRecord, error) {
	return scanOutbox(db.r.QueryRowContext(ctx, `SELECT `+outboxCols+` FROM outbox_actions
		WHERE bot_instance_id = ? AND action_id = ?`, botID, actionID))
}

// AckOutbox marks an action acked (including an expired one, since it was sent)
// and stores botMsg in the same transaction. Re-acking reports alreadyAcked.
func (db *DB) AckOutbox(ctx context.Context, botID, actionID string, ack conloomapi.AckRequest, botMsg *store.MessageRecord, now time.Time) (bool, error) {
	var alreadyAcked bool
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM outbox_actions WHERE bot_instance_id = ? AND action_id = ?`,
			botID, actionID).Scan(&status)
		if err != nil {
			return notFound(err)
		}
		if status == store.OutboxAcked {
			alreadyAcked = true
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE outbox_actions SET status = ?, platform_message_id = ?, acked_at = ?
			WHERE bot_instance_id = ? AND action_id = ?`,
			store.OutboxAcked, ack.PlatformMessageID, fmtTime(now), botID, actionID); err != nil {
			return err
		}
		if botMsg != nil {
			return insertMessage(ctx, tx, *botMsg)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return alreadyAcked, nil
}

func (db *DB) ExpireOutbox(ctx context.Context, now time.Time) (int, error) {
	res, err := db.w.ExecContext(ctx, `UPDATE outbox_actions SET status = ?
		WHERE status IN (?, ?) AND expires_at <= ?`,
		store.OutboxExpired, store.OutboxPending, store.OutboxLeased, fmtTime(now))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
