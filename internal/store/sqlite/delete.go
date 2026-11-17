package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// conversationTables are the tables whose rows belong to a conversation by
// their conversation_key. model_calls has one too but is kept for the usage
// statistics.
var conversationTables = []string{
	"messages", "tasks", "outbox_actions", "persona_instances", "persona_versions", "group_policies",
	"conversation_members", "proactive_log", "group_memory", "member_memory", "memory_revisions",
	"memory_chunks", "memory_chunk_state", "memory_chunk_rebuild", "memory_chunk_forgets",
	"conversation_status", "conversation_timers", "conversation_buffs", "conversation_buff_events",
	"conversation_custom_bars", "conversation_effects",
}

func (db *DB) DeleteConversation(ctx context.Context, convKey string) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		c, err := scanConversation(tx.QueryRowContext(ctx, `SELECT `+conversationCols+` FROM conversations
			WHERE key = ?`, convKey))
		if err != nil {
			return err
		}
		if err := deleteConversationEvents(ctx, tx, c); err != nil {
			return err
		}
		// A panel chat stores its user when it is created, before any
		// message, so another panel chat of the bot keeps the bot's panel users.
		if _, err := tx.ExecContext(ctx, `DELETE FROM users
			WHERE (bot_instance_id, platform, platform_user_id) IN (
				SELECT bot_instance_id, platform, platform_user_id FROM messages WHERE conversation_key = ?)
			AND NOT EXISTS (SELECT 1 FROM messages m
				WHERE m.bot_instance_id = users.bot_instance_id AND m.platform = users.platform
				AND m.platform_user_id = users.platform_user_id AND m.conversation_key <> ?)
			AND NOT (users.platform = ? AND EXISTS (SELECT 1 FROM conversations c
				WHERE c.bot_instance_id = users.bot_instance_id AND c.platform = ? AND c.key <> ?))`,
			convKey, convKey, store.PlatformPanel, store.PlatformPanel, convKey); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_fts
			WHERE rowid IN (SELECT seq FROM messages WHERE conversation_key = ?)`, convKey); err != nil {
			return err
		}
		for _, table := range conversationTables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_key = ?`, convKey); err != nil {
				return err
			}
		}
		// The chat's status_return timer went with its timers above.
		if scope := store.MemoryScopeKey(convKey); store.IsSharedScope(scope) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_status
				WHERE conversation_key = ? AND origin_conversation_key = ?`, scope, convKey); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM conversations WHERE key = ?`, convKey)
		return err
	})
}

// DeleteMessagesFrom deletes the conversation's messages from fromSeq onward,
// their keyword index rows, the chunks that reach into the range and the
// timers anchored there. Straddling chunks are queued for rebuild from the
// messages that remain.
func (db *DB) DeleteMessagesFrom(ctx context.Context, convKey string, fromSeq int64) (int, error) {
	var n int
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := scanConversation(tx.QueryRowContext(ctx, `SELECT `+conversationCols+` FROM conversations
			WHERE key = ?`, convKey)); err != nil {
			return err
		}
		var err error
		n, err = deleteMessagesFrom(ctx, tx, store.MessageDelete{ConversationKey: convKey, FromSeq: fromSeq})
		return err
	})
	return n, err
}

// deleteMessagesFrom is DeleteMessagesFrom's work in an existing transaction.
func deleteMessagesFrom(ctx context.Context, tx *sql.Tx, del store.MessageDelete) (int, error) {
	if del.FromSeq < 1 {
		return 0, fmt.Errorf("delete messages: from seq must be positive, got %d", del.FromSeq)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_fts
		WHERE rowid IN (SELECT seq FROM messages WHERE conversation_key = ? AND seq >= ?)`,
		del.ConversationKey, del.FromSeq); err != nil {
		return 0, err
	}
	if err := dropChunks(ctx, tx,
		`c.conversation_key = ? AND c.end_seq >= ?`, []any{del.ConversationKey, del.FromSeq},
		`c.conversation_key = ? AND c.start_seq < ? AND c.end_seq >= ?`,
		[]any{del.ConversationKey, del.FromSeq, del.FromSeq}); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_chunk_rebuild
		WHERE conversation_key = ? AND start_seq >= ?`, del.ConversationKey, del.FromSeq); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_timers
		WHERE conversation_key = ? AND anchor_seq >= ?`, del.ConversationKey, del.FromSeq); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE conversation_key = ? AND seq >= ?`,
		del.ConversationKey, del.FromSeq)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// scopeTables are the tables holding rows of a shared panel memory scope by
// their conversation_key.
var scopeTables = []string{
	"group_memory", "member_memory", "memory_revisions",
	"memory_chunks", "memory_chunk_state", "memory_chunk_rebuild", "memory_chunk_forgets",
	"conversation_status", "conversation_timers", "conversation_buffs", "conversation_buff_events",
	"conversation_custom_bars", "conversation_effects",
}

func (db *DB) DeleteScope(ctx context.Context, scopeKey string) error {
	if !store.IsSharedScope(scopeKey) {
		return fmt.Errorf("delete scope: %s is not a shared panel memory scope", scopeKey)
	}
	botID, _, _ := strings.Cut(scopeKey, ":")
	return db.inTx(ctx, func(tx *sql.Tx) error {
		chats, err := panelChatKeys(ctx, tx, botID)
		if err != nil {
			return err
		}
		if len(chats) > 0 {
			return store.ErrBotHasData
		}
		for _, table := range scopeTables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_key = ?`, scopeKey); err != nil {
				return err
			}
		}
		return nil
	})
}

// deleteConversationEvents deletes the events of c, before its tasks are
// deleted: those whose stored body decodes, as the gateway decodes it, to an
// event of c, and those whose stored response names one of c's tasks (which
// covers internal events without a body). Only bodies holding c's ID
// verbatim are decoded. Events of ignored messages keep neither a body nor a
// task and stay until they are purged.
func deleteConversationEvents(ctx context.Context, tx *sql.Tx, c store.ConversationRecord) error {
	rows, err := tx.QueryContext(ctx, `SELECT idempotency_key, raw_event FROM events
		WHERE bot_instance_id = ? AND instr(raw_event, ?) > 0`, c.BotInstanceID, c.ID)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		var ev conloomapi.Event
		if json.Unmarshal([]byte(raw), &ev) != nil {
			continue
		}
		if store.ConversationKey(c.BotInstanceID, ev.Platform, ev.Conversation.Type, ev.Conversation.ID) == c.Key {
			keys = append(keys, key)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE bot_instance_id = ? AND idempotency_key = ?`,
			c.BotInstanceID, key); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM events WHERE bot_instance_id = ?
		AND CASE WHEN json_valid(response) THEN json_extract(response, '$.task_id') END IN (
			SELECT id FROM tasks WHERE conversation_key = ?)`, c.BotInstanceID, c.Key)
	return err
}
