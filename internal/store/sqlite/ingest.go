package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func (db *DB) Ingest(ctx context.Context, in store.Ingest) (*store.EventRecord, error) {
	var existing *store.EventRecord
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		ev, err := scanEvent(tx.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events
			WHERE bot_instance_id = ? AND idempotency_key = ?`, in.Event.BotInstanceID, in.Event.IdempotencyKey))
		if err == nil {
			existing = &ev
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := insertEvent(ctx, tx, in.Event); err != nil {
			return err
		}
		if in.Conversation != nil {
			if err := upsertConversation(ctx, tx, *in.Conversation); err != nil {
				return err
			}
		}
		if in.User != nil {
			if err := upsertUser(ctx, tx, *in.User); err != nil {
				return err
			}
		}
		if in.Message != nil {
			if err := insertMessage(ctx, tx, *in.Message); err != nil {
				return err
			}
		}
		if in.NewTask != nil {
			if err := insertTask(ctx, tx, *in.NewTask); err != nil {
				return err
			}
		}
		if in.MergeIntoTaskID != "" {
			if in.Message == nil {
				return errors.New("ingest: MergeIntoTaskID requires a message")
			}
			return appendTrigger(ctx, tx, in.Event.BotInstanceID, in.MergeIntoTaskID, in.Message.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return existing, nil
}

const eventCols = `bot_instance_id, idempotency_key, request_hash, event_id, response, raw_event, created_at`

func scanEvent(row *sql.Row) (store.EventRecord, error) {
	var ev store.EventRecord
	var response, raw, created string
	err := row.Scan(&ev.BotInstanceID, &ev.IdempotencyKey, &ev.RequestHash, &ev.EventID, &response, &raw, &created)
	if err != nil {
		return ev, notFound(err)
	}
	ev.Response, ev.RawEvent = []byte(response), []byte(raw)
	ev.CreatedAt, err = parseTime(created)
	return ev, err
}

func insertEvent(ctx context.Context, tx *sql.Tx, ev store.EventRecord) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO events (`+eventCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.BotInstanceID, ev.IdempotencyKey, ev.RequestHash, ev.EventID,
		string(ev.Response), string(ev.RawEvent), fmtTime(nowIfZero(ev.CreatedAt)))
	return err
}

func upsertConversation(ctx context.Context, tx *sql.Tx, c store.ConversationRecord) error {
	if c.Key == "" {
		c.Key = store.ConversationKey(c.BotInstanceID, c.Platform, c.Type, c.ID)
	}
	now := fmtTime(time.Now())
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversations (bot_instance_id, id, key, type, platform, platform_conversation_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (bot_instance_id, id) DO UPDATE SET updated_at = excluded.updated_at`,
		c.BotInstanceID, c.ID, c.Key, c.Type, c.Platform, c.PlatformConversationID, now, now)
	return err
}

func upsertUser(ctx context.Context, tx *sql.Tx, u store.UserRecord) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO users (bot_instance_id, platform, platform_user_id, display_name, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (bot_instance_id, platform, platform_user_id)
		DO UPDATE SET display_name = excluded.display_name, updated_at = excluded.updated_at`,
		u.BotInstanceID, u.Platform, u.PlatformUserID, u.DisplayName, fmtTime(time.Now()))
	return err
}

const messageCols = `seq, bot_instance_id, id, conversation_key, platform, platform_message_id, actor_id,
	platform_user_id, display_name, text, reply_to, mentions, attachments, from_bot, occurred_at, created_at`

func insertMessage(ctx context.Context, tx *sql.Tx, m store.MessageRecord) error {
	err := execInsertMessage(ctx, tx, m, "")
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %v", store.ErrDuplicateMessage, err)
	}
	return err
}

// insertMessageIfAbsent inserts m unless it clashes with an existing message's
// ID or platform message ID, in which case the existing row is kept.
func insertMessageIfAbsent(ctx context.Context, tx *sql.Tx, m store.MessageRecord) error {
	return execInsertMessage(ctx, tx, m, " ON CONFLICT DO NOTHING")
}

// execInsertMessage inserts m and, when it was inserted, its keyword index
// row.
func execInsertMessage(ctx context.Context, tx *sql.Tx, m store.MessageRecord, onConflict string) error {
	mentions, err := json.Marshal(m.Mentions)
	if err != nil {
		return err
	}
	attachments, err := json.Marshal(m.Attachments)
	if err != nil {
		return err
	}
	var seq int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO messages (bot_instance_id, id, conversation_key, platform, platform_message_id, actor_id,
			platform_user_id, display_name, text, reply_to, mentions, attachments, from_bot, occurred_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`+onConflict+` RETURNING seq`,
		m.BotInstanceID, m.ID, m.ConversationKey, m.Platform, m.PlatformMessageID, m.ActorID,
		m.PlatformUserID, m.DisplayName, m.Text, m.ReplyTo, string(mentions), string(attachments), m.FromBot,
		fmtTime(m.OccurredAt), fmtTime(nowIfZero(m.CreatedAt))).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return indexMessage(ctx, tx, seq, m.ConversationKey, m.DisplayName, m.Text)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMessage(row scanner) (store.MessageRecord, error) {
	var m store.MessageRecord
	var mentions, attachments, occurred, created string
	err := row.Scan(&m.Seq, &m.BotInstanceID, &m.ID, &m.ConversationKey, &m.Platform, &m.PlatformMessageID, &m.ActorID,
		&m.PlatformUserID, &m.DisplayName, &m.Text, &m.ReplyTo, &mentions, &attachments, &m.FromBot, &occurred, &created)
	if err != nil {
		return m, notFound(err)
	}
	if err := json.Unmarshal([]byte(mentions), &m.Mentions); err != nil {
		return m, err
	}
	if err := json.Unmarshal([]byte(attachments), &m.Attachments); err != nil {
		return m, err
	}
	if m.OccurredAt, err = parseTime(occurred); err != nil {
		return m, err
	}
	m.CreatedAt, err = parseTime(created)
	return m, err
}

func (db *DB) queryMessages(ctx context.Context, query string, args ...any) ([]store.MessageRecord, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.MessageRecord
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (db *DB) GetEvent(ctx context.Context, botID, key string) (store.EventRecord, error) {
	return scanEvent(db.r.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events
		WHERE bot_instance_id = ? AND idempotency_key = ?`, botID, key))
}

const conversationCols = `bot_instance_id, id, key, type, platform, platform_conversation_id`

func scanConversation(row scanner) (store.ConversationRecord, error) {
	var c store.ConversationRecord
	err := row.Scan(&c.BotInstanceID, &c.ID, &c.Key, &c.Type, &c.Platform, &c.PlatformConversationID)
	return c, notFound(err)
}

func (db *DB) GetConversation(ctx context.Context, botID, conversationID string) (store.ConversationRecord, error) {
	return scanConversation(db.r.QueryRowContext(ctx, `SELECT `+conversationCols+` FROM conversations
		WHERE bot_instance_id = ? AND id = ?`, botID, conversationID))
}

func (db *DB) GetMessage(ctx context.Context, botID, id string) (store.MessageRecord, error) {
	return scanMessage(db.r.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE bot_instance_id = ? AND id = ?`, botID, id))
}

func (db *DB) GetMessages(ctx context.Context, botID string, ids []string) ([]store.MessageRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, botID)
	for _, id := range ids {
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	return db.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
		WHERE bot_instance_id = ? AND id IN (`+placeholders+`) ORDER BY seq`, args...)
}

func (db *DB) LatestDisplayNames(ctx context.Context, conversationKey string, actorIDs []string) (map[string]string, error) {
	return db.latestPerActor(ctx, conversationKey, actorIDs, "display_name")
}

// maxCompoundSelect stays below SQLite's default limit of 500 terms in a
// compound SELECT.
const maxCompoundSelect = 200

// latestPerActor maps each of actorIDs to the non-empty messages column col
// of its newest member message in the conversation; actors without one are
// absent.
func (db *DB) latestPerActor(ctx context.Context, conversationKey string, actorIDs []string, col string) (map[string]string, error) {
	out := map[string]string{}
	for chunk := range slices.Chunk(actorIDs, maxCompoundSelect) {
		if err := db.latestPerActorChunk(ctx, conversationKey, chunk, col, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// latestPerActorChunk looks up each actor's newest value with its own indexed
// LIMIT 1 query, combined into one statement.
func (db *DB) latestPerActorChunk(ctx context.Context, conversationKey string, actorIDs []string, col string, out map[string]string) error {
	parts := make([]string, len(actorIDs))
	args := make([]any, 0, 2*len(actorIDs))
	for i, id := range actorIDs {
		parts[i] = `SELECT * FROM (SELECT actor_id, ` + col + ` FROM messages
			WHERE conversation_key = ? AND actor_id = ? AND from_bot = 0 AND ` + col + ` <> ''
			ORDER BY seq DESC LIMIT 1)`
		args = append(args, conversationKey, id)
	}
	rows, err := db.r.QueryContext(ctx, strings.Join(parts, " UNION ALL "), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil {
			return err
		}
		out[id] = value
	}
	return rows.Err()
}

func (db *DB) RecentMessages(ctx context.Context, conversationKey string, limit int) ([]store.MessageRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	return db.queryMessages(ctx, `SELECT * FROM (
		SELECT `+messageCols+` FROM messages WHERE conversation_key = ? ORDER BY seq DESC LIMIT ?
	) ORDER BY seq`, conversationKey, limit)
}

func (db *DB) PurgeEvents(ctx context.Context, before time.Time) (int, error) {
	res, err := db.w.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, fmtTime(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
