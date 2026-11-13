package sqlite

import (
	"context"
	"strings"

	"github.com/kinako404/conloom/internal/store"
)

// maxPanelLimit caps the rows returned by one panel list query.
const maxPanelLimit = 200

func panelLimit(limit int) int {
	return min(max(limit, 0), maxPanelLimit)
}

// queryAll runs query on the read pool and decodes every row with scan.
func queryAll[T any](ctx context.Context, db *DB, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (db *DB) GetConversationByKey(ctx context.Context, key string) (store.ConversationRecord, error) {
	return scanConversation(db.r.QueryRowContext(ctx, `SELECT `+conversationCols+` FROM conversations WHERE key = ?`, key))
}

// ConversationStats lists every conversation with its message count and the
// time of its newest message, most recently active first; conversations
// without messages come last, by key.
func (db *DB) ConversationStats(ctx context.Context) ([]store.ConversationStats, error) {
	return queryAll(ctx, db, scanConversationStats, `
		SELECT c.bot_instance_id, c.id, c.key, c.type, c.platform, c.platform_conversation_id,
			COALESCE(s.n, 0), COALESCE((SELECT occurred_at FROM messages WHERE seq = s.last_seq), '') AS last_at
		FROM conversations c
		LEFT JOIN (SELECT conversation_key, COUNT(*) AS n, MAX(seq) AS last_seq FROM messages GROUP BY conversation_key) s
			ON s.conversation_key = c.key
		ORDER BY last_at = '', last_at DESC, c.key`)
}

func scanConversationStats(row scanner) (store.ConversationStats, error) {
	var s store.ConversationStats
	var last string
	err := row.Scan(&s.BotInstanceID, &s.ID, &s.Key, &s.Type, &s.Platform, &s.PlatformConversationID,
		&s.MessageCount, &last)
	if err != nil {
		return s, err
	}
	s.LastMessageAt, err = parseTime(last)
	return s, err
}

// MessagesBefore pages back through a conversation newest first: messages
// with seq below beforeSeq, or the newest ones when beforeSeq <= 0.
func (db *DB) MessagesBefore(ctx context.Context, convKey string, beforeSeq int64, limit int) ([]store.MessageRecord, error) {
	limit = panelLimit(limit)
	if limit == 0 {
		return nil, nil
	}
	if beforeSeq <= 0 {
		return db.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
			WHERE conversation_key = ? ORDER BY seq DESC LIMIT ?`, convKey, limit)
	}
	return db.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
		WHERE conversation_key = ? AND seq < ? ORDER BY seq DESC LIMIT ?`, convKey, beforeSeq, limit)
}

// MessagesBySeq maps each of seqs that belongs to the conversation to its
// message; other seqs are absent.
func (db *DB) MessagesBySeq(ctx context.Context, convKey string, seqs []int64) (map[int64]store.MessageRecord, error) {
	out := map[int64]store.MessageRecord{}
	if len(seqs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(seqs)+1)
	args = append(args, convKey)
	for _, s := range seqs {
		args = append(args, s)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(seqs)), ", ")
	msgs, err := db.queryMessages(ctx, `SELECT `+messageCols+` FROM messages
		WHERE conversation_key = ? AND seq IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		out[m.Seq] = m
	}
	return out, nil
}

// whereClause collects equality conditions, skipping empty values.
type whereClause struct {
	conds []string
	args  []any
}

func (w *whereClause) add(cond, value string) {
	if value != "" {
		w.conds = append(w.conds, cond)
		w.args = append(w.args, value)
	}
}

func (w *whereClause) String() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

func (db *DB) ListTasks(ctx context.Context, f store.TaskFilter) ([]store.TaskRecord, error) {
	limit := panelLimit(f.Limit)
	if limit == 0 {
		return nil, nil
	}
	var w whereClause
	w.add("status = ?", f.Status)
	w.add("bot_instance_id = ?", f.BotInstanceID)
	w.add("conversation_key = ?", f.ConversationKey)
	w.add("id < ?", f.Before)
	return queryAll(ctx, db, scanTask, `SELECT `+taskCols+` FROM tasks`+w.String()+` ORDER BY id DESC LIMIT ?`,
		append(w.args, limit)...)
}

const auditCols = `id, bot_instance_id, kind, actor, target, detail, created_at`

func scanAudit(row scanner) (store.AuditEvent, error) {
	var a store.AuditEvent
	var created string
	err := row.Scan(&a.ID, &a.BotInstanceID, &a.Kind, &a.Actor, &a.Target, &a.Detail, &created)
	if err != nil {
		return a, notFound(err)
	}
	a.CreatedAt, err = parseTime(created)
	return a, err
}

func (db *DB) ListAudit(ctx context.Context, f store.AuditFilter) ([]store.AuditEvent, error) {
	limit := panelLimit(f.Limit)
	if limit == 0 {
		return nil, nil
	}
	var w whereClause
	w.add("kind = ?", f.Kind)
	w.add("bot_instance_id = ?", f.BotInstanceID)
	w.add("id < ?", f.Before)
	return queryAll(ctx, db, scanAudit, `SELECT `+auditCols+` FROM audit_events`+w.String()+` ORDER BY id DESC LIMIT ?`,
		append(w.args, limit)...)
}

func (db *DB) ListPersonaVersions(ctx context.Context, convKey string) ([]store.PersonaVersion, error) {
	return queryAll(ctx, db, scanPersonaVersion, `SELECT `+personaVersionCols+personaVersionFrom+`
		WHERE v.conversation_key = ? ORDER BY v.version DESC`, convKey)
}

// CountPendingOutbox counts actions not yet delivered: pending or leased.
func (db *DB) CountPendingOutbox(ctx context.Context) (int, error) {
	var n int
	err := db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_actions WHERE status IN (?, ?)`,
		store.OutboxPending, store.OutboxLeased).Scan(&n)
	return n, err
}
