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
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const groupMemoryCols = `conversation_key, summary, cursor_seq, last_run_at, last_error, updated_at`

func (db *DB) GetGroupMemory(ctx context.Context, convKey string) (store.GroupMemory, error) {
	var g store.GroupMemory
	var runAt, updated string
	err := db.r.QueryRowContext(ctx, `SELECT `+groupMemoryCols+` FROM group_memory WHERE conversation_key = ?`, convKey).
		Scan(&g.ConversationKey, &g.Summary, &g.CursorSeq, &runAt, &g.LastError, &updated)
	if err != nil {
		return store.GroupMemory{}, notFound(err)
	}
	if g.LastRunAt, err = parseTime(runAt); err != nil {
		return store.GroupMemory{}, err
	}
	if g.UpdatedAt, err = parseTime(updated); err != nil {
		return store.GroupMemory{}, err
	}
	return g, nil
}

// PutGroupMemory stores g as the conversation's row if it has none, and
// otherwise only moves the stored cursor forward to g.CursorSeq, keeping the
// rest; UpdatedAt is stored as now. A row read before a forget
// (ForgetPrivateScope) committed thus cannot bring back the cleared summary
// or the cursor from before it.
func (db *DB) PutGroupMemory(ctx context.Context, g store.GroupMemory) error {
	_, err := db.w.ExecContext(ctx, `INSERT INTO group_memory (`+groupMemoryCols+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (conversation_key) DO UPDATE SET cursor_seq = `+cursorForward+`, updated_at = excluded.updated_at`,
		g.ConversationKey, g.Summary, g.CursorSeq, fmtTime(g.LastRunAt), g.LastError, fmtTime(time.Now()))
	return err
}

// putGroupMemory upserts g, setting the cursor of an existing row to cursorExpr.
func putGroupMemory(ctx context.Context, tx *sql.Tx, g store.GroupMemory, cursorExpr string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO group_memory (`+groupMemoryCols+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (conversation_key) DO UPDATE SET
			summary = excluded.summary, cursor_seq = `+cursorExpr+`, last_run_at = excluded.last_run_at,
			last_error = excluded.last_error, updated_at = excluded.updated_at`,
		g.ConversationKey, g.Summary, g.CursorSeq, fmtTime(g.LastRunAt), g.LastError, fmtTime(time.Now()))
	return err
}

// cursorForward keeps the larger of the stored and the written cursor.
const cursorForward = "MAX(group_memory.cursor_seq, excluded.cursor_seq)"

const memberMemoryCols = `conversation_key, platform_user_id, display_name, note, affinity, last_interaction_day,
	model_delta_day, model_delta_sum, decay_steps, forgotten_at, version, updated_at`

func scanMemberMemory(row scanner) (store.MemberMemory, error) {
	var m store.MemberMemory
	var affinity sql.NullInt64
	var forgotten, updated string
	err := row.Scan(&m.ConversationKey, &m.PlatformUserID, &m.DisplayName, &m.Note, &affinity, &m.LastInteractionDay,
		&m.ModelDeltaDay, &m.ModelDeltaSum, &m.DecaySteps, &forgotten, &m.Version, &updated)
	if err != nil {
		return m, notFound(err)
	}
	if affinity.Valid {
		v := int(affinity.Int64)
		m.Affinity = &v
	}
	if m.ForgottenAt, err = parseTime(forgotten); err != nil {
		return m, err
	}
	m.UpdatedAt, err = parseTime(updated)
	return m, err
}

func (db *DB) queryMemberMemories(ctx context.Context, query string, args ...any) ([]store.MemberMemory, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.MemberMemory
	for rows.Next() {
		m, err := scanMemberMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (db *DB) GetMemberMemories(ctx context.Context, convKey string, userIDs []string) (map[string]store.MemberMemory, error) {
	out := map[string]store.MemberMemory{}
	if len(userIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(userIDs)+1)
	args = append(args, convKey)
	for _, id := range userIDs {
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(userIDs)), ", ")
	list, err := db.queryMemberMemories(ctx, `SELECT `+memberMemoryCols+` FROM member_memory
		WHERE conversation_key = ? AND platform_user_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		out[m.PlatformUserID] = m
	}
	return out, nil
}

func (db *DB) ListMemberMemories(ctx context.Context, convKey string) ([]store.MemberMemory, error) {
	return db.queryMemberMemories(ctx, `SELECT `+memberMemoryCols+` FROM member_memory
		WHERE conversation_key = ? ORDER BY platform_user_id`, convKey)
}

func (db *DB) ApplyMemoryWrite(ctx context.Context, w store.MemoryWrite) ([]string, error) {
	if err := checkMemoryWrite(w); err != nil {
		return nil, err
	}
	var conflicts []string
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		conflicts = nil
		keepSummary, err := summaryGuardBroken(ctx, tx, w)
		if err != nil {
			return err
		}
		keepSummary = keepSummary || w.KeepSummary
		if w.Group != nil {
			g := *w.Group
			if keepSummary {
				if g.Summary, err = storedSummary(ctx, tx, w.ConversationKey); err != nil {
					return err
				}
			}
			if err := putGroupMemory(ctx, tx, g, cursorForward); err != nil {
				return err
			}
		}
		now := fmtTime(time.Now())
		for _, m := range w.Members {
			written, err := writeMemberMemory(ctx, tx, m, now)
			if err != nil {
				return err
			}
			if !written {
				conflicts = append(conflicts, m.PlatformUserID)
			}
		}
		for _, r := range w.Revisions {
			if r.PlatformUserID != "" && slices.Contains(conflicts, r.PlatformUserID) ||
				keepSummary && r.PlatformUserID == "" && r.Field == store.FieldSummary {
				continue
			}
			if err := insertMemoryRevision(ctx, tx, r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conflicts, nil
}

// summaryGuardBroken reports whether a member of w.SummaryGuards no longer
// has the version given.
func summaryGuardBroken(ctx context.Context, tx *sql.Tx, w store.MemoryWrite) (bool, error) {
	for _, gd := range w.SummaryGuards {
		var v int64
		err := tx.QueryRowContext(ctx, `SELECT version FROM member_memory WHERE conversation_key = ? AND platform_user_id = ?`,
			w.ConversationKey, gd.PlatformUserID).Scan(&v)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if v != gd.Version {
			return true, nil
		}
	}
	return false, nil
}

func storedSummary(ctx context.Context, tx *sql.Tx, convKey string) (string, error) {
	var summary string
	err := tx.QueryRowContext(ctx, `SELECT summary FROM group_memory WHERE conversation_key = ?`, convKey).Scan(&summary)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return summary, err
}

func (db *DB) ForgetPrivate(ctx context.Context, convKey, userID string, at time.Time) error {
	return db.ForgetPrivateScope(ctx, convKey, []string{convKey}, userID, at)
}

func (db *DB) ForgetPrivateScope(ctx context.Context, scopeKey string, chats []string, userID string, at time.Time) error {
	if userID == "" {
		return errors.New("forget private member: empty user ID")
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if err := forgetMember(ctx, tx, scopeKey, userID, at); err != nil {
			return err
		}
		for _, chat := range chats {
			if err := forgetPrivateChunks(ctx, tx, chat, userID, at); err != nil {
				return err
			}
			if err := deleteAutonomy(ctx, tx, chat); err != nil {
				return err
			}
		}
		// The scope's own record matches its member row, so reconciling
		// (ReconcileForgottenChunks) knows its chats' chunks saw this forget.
		// The scope holds the status, bars, bar changes, effects and scene of
		// its chats.
		if !slices.Contains(chats, scopeKey) {
			if err := chunksForgot(ctx, tx, scopeKey, userID, at); err != nil {
				return err
			}
			if err := deleteAutonomy(ctx, tx, scopeKey); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_revisions WHERE conversation_key = ? AND platform_user_id = '' AND field = ?`,
			scopeKey, store.FieldSummary); err != nil {
			return err
		}
		var newest int64
		if len(chats) > 0 {
			in, args := inList(chats)
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM messages WHERE conversation_key IN (`+in+`)`,
				args...).Scan(&newest); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO group_memory (`+groupMemoryCols+`) VALUES (?, '', ?, ?, '', ?)
			ON CONFLICT (conversation_key) DO UPDATE SET summary = '', cursor_seq = `+cursorForward+`,
				updated_at = excluded.updated_at`,
			scopeKey, newest, fmtTime(time.Time{}), fmtTime(time.Now()))
		return err
	})
}

func checkMemoryWrite(w store.MemoryWrite) error {
	if w.Group != nil && w.Group.ConversationKey != w.ConversationKey {
		return fmt.Errorf("memory write for %s: group belongs to %s", w.ConversationKey, w.Group.ConversationKey)
	}
	seen := make(map[string]bool, len(w.Members))
	for _, m := range w.Members {
		if m.ConversationKey != w.ConversationKey {
			return fmt.Errorf("memory write for %s: member %s belongs to %s", w.ConversationKey, m.PlatformUserID, m.ConversationKey)
		}
		if m.PlatformUserID == "" {
			return fmt.Errorf("memory write for %s: member without user ID", w.ConversationKey)
		}
		if seen[m.PlatformUserID] {
			return fmt.Errorf("memory write for %s: member %s appears twice", w.ConversationKey, m.PlatformUserID)
		}
		seen[m.PlatformUserID] = true
	}
	for _, r := range w.Revisions {
		if r.ConversationKey != w.ConversationKey {
			return fmt.Errorf("memory write for %s: revision belongs to %s", w.ConversationKey, r.ConversationKey)
		}
	}
	return nil
}

// writeMemberMemory stores m at version m.Version+1 if the stored version is
// still m.Version (no row for 0) and reports whether it did.
func writeMemberMemory(ctx context.Context, tx *sql.Tx, m store.MemberMemory, now string) (bool, error) {
	var affinity any
	if m.Affinity != nil {
		affinity = *m.Affinity
	}
	args := []any{m.DisplayName, m.Note, affinity, m.LastInteractionDay, m.ModelDeltaDay, m.ModelDeltaSum, m.DecaySteps,
		fmtTime(m.ForgottenAt), m.Version + 1, now, m.ConversationKey, m.PlatformUserID}
	var res sql.Result
	var err error
	if m.Version == 0 {
		res, err = tx.ExecContext(ctx, `INSERT INTO member_memory (display_name, note, affinity, last_interaction_day,
			model_delta_day, model_delta_sum, decay_steps, forgotten_at, version, updated_at, conversation_key, platform_user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, args...)
	} else {
		res, err = tx.ExecContext(ctx, `UPDATE member_memory SET display_name = ?, note = ?, affinity = ?,
			last_interaction_day = ?, model_delta_day = ?, model_delta_sum = ?, decay_steps = ?, forgotten_at = ?,
			version = ?, updated_at = ?
			WHERE conversation_key = ? AND platform_user_id = ? AND version = ?`, append(args, m.Version)...)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (db *DB) ForgetMember(ctx context.Context, convKey, userID string, at time.Time) error {
	if userID == "" {
		return errors.New("forget member: empty user ID")
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if err := forgetMember(ctx, tx, convKey, userID, at); err != nil {
			return err
		}
		return forgetMemberChunks(ctx, tx, convKey, userID, at)
	})
}

// forgetMember clears the user's memory, marks it forgotten at at and deletes
// the user's revisions in the conversation.
func forgetMember(ctx context.Context, tx *sql.Tx, convKey, userID string, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO member_memory (`+memberMemoryCols+`)
		VALUES (?, ?, '', '', NULL, '', '', 0, 0, ?, 1, ?)
		ON CONFLICT (conversation_key, platform_user_id) DO UPDATE SET
			display_name = '', note = '', affinity = NULL, last_interaction_day = '', model_delta_day = '',
			model_delta_sum = 0, decay_steps = 0, forgotten_at = excluded.forgotten_at,
			version = member_memory.version + 1, updated_at = excluded.updated_at`,
		convKey, userID, fmtTime(at), fmtTime(time.Now())); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM memory_revisions WHERE conversation_key = ? AND platform_user_id = ?`,
		convKey, userID)
	return err
}

const memoryRevisionCols = `id, conversation_key, platform_user_id, field, old_value, new_value, source, reason, created_at`

// insertMemoryRevision stores r with a new ID; a zero CreatedAt is stored as now.
func insertMemoryRevision(ctx context.Context, tx *sql.Tx, r store.MemoryRevision) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_revisions
		(conversation_key, platform_user_id, field, old_value, new_value, source, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ConversationKey, r.PlatformUserID, r.Field, r.OldValue, r.NewValue, r.Source, r.Reason,
		fmtTime(nowIfZero(r.CreatedAt)))
	return err
}

func (db *DB) ListMemoryRevisions(ctx context.Context, convKey, userID string) ([]store.MemoryRevision, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+memoryRevisionCols+` FROM memory_revisions
		WHERE conversation_key = ? AND platform_user_id = ? ORDER BY id`, convKey, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.MemoryRevision
	for rows.Next() {
		var r store.MemoryRevision
		var created string
		if err := rows.Scan(&r.ID, &r.ConversationKey, &r.PlatformUserID, &r.Field, &r.OldValue, &r.NewValue,
			&r.Source, &r.Reason, &created); err != nil {
			return nil, err
		}
		if r.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) PurgeMemoryRevisions(ctx context.Context, before time.Time) (int, error) {
	res, err := db.w.ExecContext(ctx, `DELETE FROM memory_revisions WHERE created_at < ?`, fmtTime(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (db *DB) MessagesAfter(ctx context.Context, convKey string, afterSeq int64, limit int) ([]store.MessageRecord, error) {
	return db.MessagesAfterIn(ctx, []string{convKey}, afterSeq, limit)
}

func (db *DB) MessagesAfterIn(ctx context.Context, convKeys []string, afterSeq int64, limit int) ([]store.MessageRecord, error) {
	if limit <= 0 || len(convKeys) == 0 {
		return nil, nil
	}
	in, args := inList(convKeys)
	return db.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
		WHERE conversation_key IN (`+in+`) AND seq > ? ORDER BY seq LIMIT ?`, append(args, afterSeq, limit)...)
}

func (db *DB) LastMessages(ctx context.Context, convKeys []string) (map[string]store.MessageRecord, error) {
	out := map[string]store.MessageRecord{}
	if len(convKeys) == 0 {
		return out, nil
	}
	in, args := inList(convKeys)
	msgs, err := db.queryMessages(ctx, `SELECT `+messageCols+` FROM messages WHERE seq IN
		(SELECT MAX(seq) FROM messages WHERE conversation_key IN (`+in+`) GROUP BY conversation_key)`, args...)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		out[m.ConversationKey] = m
	}
	return out, nil
}

// LastBotMessageTimes walks each conversation's messages back from the newest
// on messages_conversation_seq and stops at its first bot message.
func (db *DB) LastBotMessageTimes(ctx context.Context, convKeys []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(convKeys) == 0 {
		return out, nil
	}
	keys, err := json.Marshal(convKeys)
	if err != nil {
		return nil, err
	}
	rows, err := db.r.QueryContext(ctx, `SELECT k.value, (SELECT occurred_at FROM messages
			WHERE conversation_key = k.value AND from_bot = 1 ORDER BY seq DESC LIMIT 1)
		FROM json_each(?) k`, string(keys))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var at sql.NullString
		if err := rows.Scan(&key, &at); err != nil {
			return nil, err
		}
		if !at.Valid {
			continue
		}
		t, err := parseTime(at.String)
		if err != nil {
			return nil, err
		}
		out[key] = t
	}
	return out, rows.Err()
}

func (db *DB) CountMessagesAfter(ctx context.Context, convKey string, afterSeq int64) (int, error) {
	return db.CountMessagesAfterIn(ctx, []string{convKey}, afterSeq)
}

func (db *DB) CountMessagesAfterIn(ctx context.Context, convKeys []string, afterSeq int64) (int, error) {
	if len(convKeys) == 0 {
		return 0, nil
	}
	in, args := inList(convKeys)
	var n int
	err := db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE conversation_key IN (`+in+`) AND seq > ?`,
		append(args, afterSeq)...).Scan(&n)
	return n, err
}

func (db *DB) BacklogCursor(ctx context.Context, convKey string, backlog int) (int64, error) {
	return db.BacklogCursorIn(ctx, []string{convKey}, backlog)
}

// BacklogCursorIn counts the conversations' own messages, since seq is shared
// by all conversations.
func (db *DB) BacklogCursorIn(ctx context.Context, convKeys []string, backlog int) (int64, error) {
	if len(convKeys) == 0 {
		return 0, nil
	}
	in, args := inList(convKeys)
	var seq int64
	err := db.r.QueryRowContext(ctx, `SELECT seq FROM messages WHERE conversation_key IN (`+in+`)
		ORDER BY seq DESC LIMIT 1 OFFSET ?`, append(args, backlog)...).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func (db *DB) LatestChat(ctx context.Context, convKeys []string) (string, error) {
	if len(convKeys) == 0 {
		return "", nil
	}
	in, args := inList(convKeys)
	var key string
	err := db.r.QueryRowContext(ctx, `SELECT conversation_key FROM messages
		WHERE seq = (SELECT MAX(seq) FROM messages WHERE conversation_key IN (`+in+`))`, args...).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
}

func (db *DB) PanelChatKeys(ctx context.Context, botID string) ([]string, error) {
	return panelChatKeys(ctx, db.r, botID)
}

func panelChatKeys(ctx context.Context, q rowsQueryer, botID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key FROM conversations
		WHERE bot_instance_id = ? AND platform = ? AND type = ? ORDER BY key`, botID, store.PlatformPanel, conloomapi.ConvPrivate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (db *DB) UserIDsForActors(ctx context.Context, convKey string, actorIDs []string) (map[string]string, error) {
	return db.latestPerActor(ctx, convKey, actorIDs, "platform_user_id")
}

func (db *DB) ListConversations(ctx context.Context, convType string) ([]store.ConversationRecord, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+conversationCols+` FROM conversations
		WHERE type = ? ORDER BY key`, convType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ConversationRecord
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
