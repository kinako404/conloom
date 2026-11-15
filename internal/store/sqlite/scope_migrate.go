package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// scopeMergeTables are the tables whose rows under a panel chat's own key
// MigratePanelMemoryScope merges into the chat's shared memory scope.
var scopeMergeTables = []string{
	"group_memory", "member_memory", "memory_revisions", "conversation_status", "conversation_buffs", "conversation_buff_events",
}

// MigratePanelMemoryScope merges the memory that older versions kept under
// each panel chat's own key into its bot's shared panel memory scope
// (store.MemoryScopeKey), in one transaction that ends by recording now under
// store.MetaPanelMemoryScoped. Of the rows under a bot's panel chats, and any
// already under its scope:
//   - the summary is the one consolidated last (latest last_run_at), and the
//     cursor the largest, so nothing a chat consolidated is consolidated again;
//   - each member keeps the note and affinity of the row updated last, and the
//     latest forget time of any of its rows, so no forget is undone;
//   - every revision and bar change is kept;
//   - the status is the one ending last, still marked as set in its chat,
//     whose status_return timer stays; the other chats' status_return timers
//     are deleted;
//   - each bar keeps the value updated last.
//
// Semantic search chunks stay with their chats, which the scope's searches
// cover. Rows of any other conversation are not touched. It returns, ordered
// by bot, the bots whose panel chats had memory with those chats. Once the
// fact is recorded it changes nothing and reports ran false.
func (db *DB) MigratePanelMemoryScope(ctx context.Context, now time.Time) (merged []store.ScopeMerge, ran bool, err error) {
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		merged = nil
		var done bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM app_meta WHERE key = ?)`,
			store.MetaPanelMemoryScoped).Scan(&done); err != nil {
			return err
		}
		if done {
			return nil
		}
		bots, err := panelChatBots(ctx, tx)
		if err != nil {
			return err
		}
		for _, bot := range bots {
			chats, err := panelChatKeys(ctx, tx, bot)
			if err != nil {
				return err
			}
			moved, err := mergeIntoScope(ctx, tx, store.PanelScopeKey(bot), chats)
			if err != nil {
				return err
			}
			if len(moved) > 0 {
				merged = append(merged, store.ScopeMerge{BotInstanceID: bot, Chats: moved})
			}
		}
		ran = true
		_, err = tx.ExecContext(ctx, `INSERT INTO app_meta (key, value, updated_at) VALUES (?, ?, ?)`,
			store.MetaPanelMemoryScoped, fmtTime(now), fmtTime(now))
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return merged, ran, nil
}

// panelChatBots returns the bots that have panel chats, ordered by ID.
func panelChatBots(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT bot_instance_id FROM conversations
		WHERE platform = ? AND type = ? ORDER BY bot_instance_id`, store.PlatformPanel, conloomapi.ConvPrivate)
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, id)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// mergeIntoScope merges the memory rows of chats into scope as
// MigratePanelMemoryScope describes and returns the chats that had any,
// ordered by key.
func mergeIntoScope(ctx context.Context, tx *sql.Tx, scope string, chats []string) ([]string, error) {
	moved, err := chatsWithMemory(ctx, tx, chats)
	if err != nil || len(moved) == 0 {
		return nil, err
	}
	keys := append(slices.Clone(chats), scope)
	in, args := inList(keys)
	var cursor int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(cursor_seq), 0) FROM group_memory WHERE conversation_key IN (`+in+`)`,
		args...).Scan(&cursor); err != nil {
		return nil, err
	}
	if err := keepNewest(ctx, tx, "group_memory", "", "last_run_at DESC, updated_at DESC", keys, scope, ""); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE group_memory SET cursor_seq = ? WHERE conversation_key = ?`, cursor, scope); err != nil {
		return nil, err
	}
	// Every row of a member gets the latest forget time of any of them, so the
	// row kept for its note and affinity keeps that forget too.
	if _, err := tx.ExecContext(ctx, `UPDATE member_memory SET forgotten_at = (SELECT MAX(m.forgotten_at) FROM member_memory m
		WHERE m.conversation_key IN (`+in+`) AND m.platform_user_id = member_memory.platform_user_id)
		WHERE conversation_key IN (`+in+`)`, append(slices.Clone(args), args...)...); err != nil {
		return nil, err
	}
	if err := keepNewest(ctx, tx, "member_memory", "platform_user_id", "updated_at DESC, version DESC", keys, scope, ""); err != nil {
		return nil, err
	}
	if err := keepNewest(ctx, tx, "conversation_status", "", "until_at DESC, updated_at DESC", keys, scope,
		`origin_conversation_key = CASE origin_conversation_key WHEN '' THEN conversation_key ELSE origin_conversation_key END, `); err != nil {
		return nil, err
	}
	chatsIn, chatArgs := inList(chats)
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_timers WHERE kind = ? AND conversation_key IN (`+chatsIn+`)
		AND conversation_key <> COALESCE((SELECT origin_conversation_key FROM conversation_status WHERE conversation_key = ?), '')`,
		append(append([]any{string(store.TimerStatusReturn)}, chatArgs...), scope)...); err != nil {
		return nil, err
	}
	if err := keepNewest(ctx, tx, "conversation_buffs", "name", "updated_at DESC", keys, scope, ""); err != nil {
		return nil, err
	}
	for _, table := range []string{"memory_revisions", "conversation_buff_events"} {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET conversation_key = ? WHERE conversation_key IN (`+chatsIn+`)`,
			append([]any{scope}, chatArgs...)...); err != nil {
			return nil, err
		}
	}
	return moved, nil
}

// chatsWithMemory returns those of chats that have rows in scopeMergeTables,
// ordered by key.
func chatsWithMemory(ctx context.Context, tx *sql.Tx, chats []string) ([]string, error) {
	if len(chats) == 0 {
		return nil, nil
	}
	in, args := inList(chats)
	var parts []string
	var all []any
	for _, table := range scopeMergeTables {
		parts = append(parts, `SELECT conversation_key FROM `+table+` WHERE conversation_key IN (`+in+`)`)
		all = append(all, args...)
	}
	rows, err := tx.QueryContext(ctx, strings.Join(parts, " UNION ")+` ORDER BY conversation_key`, all...)
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, key)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// keepNewest keeps, of table's rows under keys, the first of each partition
// (by the columns partition, or all of them when it is empty) in order, the
// key breaking ties, deletes the others and moves the kept ones under scope,
// also applying set ("column = expression, " clauses, evaluated on the row as
// it was).
func keepNewest(ctx context.Context, tx *sql.Tx, table, partition, order string, keys []string, scope, set string) error {
	in, args := inList(keys)
	over := "ORDER BY " + order + ", conversation_key"
	if partition != "" {
		over = "PARTITION BY " + partition + " " + over
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_key IN (`+in+`) AND rowid NOT IN (
		SELECT id FROM (SELECT rowid AS id, ROW_NUMBER() OVER (`+over+`) AS n FROM `+table+`
			WHERE conversation_key IN (`+in+`)) WHERE n = 1)`, append(slices.Clone(args), args...)...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE `+table+` SET `+set+`conversation_key = ? WHERE conversation_key IN (`+in+`)`,
		append([]any{scope}, args...)...)
	return err
}
