package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
)

// indexMessage adds or replaces the keyword index row of the message seq.
func indexMessage(ctx context.Context, tx *sql.Tx, seq int64, convKey, displayName, text string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO message_fts (rowid, seg) VALUES (?, ?)`,
		seq, search.IndexText(convKey, displayName, text))
	return err
}

// matchExpr is the FTS5 query for the conversation's messages holding any of
// terms, each looked up as its search.TermPhrase; it is empty when no term
// has a phrase. Tokens hold only letters and digits, so quoting them is
// enough to keep FTS5 syntax out.
func matchExpr(convKey string, terms []string) string {
	var phrases []string
	for _, term := range terms {
		tokens, prefix := search.TermPhrase(term)
		if len(tokens) == 0 {
			continue
		}
		p := `"` + strings.Join(tokens, " ") + `"`
		if prefix {
			p += "*"
		}
		if !slices.Contains(phrases, p) {
			phrases = append(phrases, p)
		}
	}
	if len(phrases) == 0 {
		return ""
	}
	return `"` + search.ConversationMark(convKey) + `" AND (` + strings.Join(phrases, " OR ") + `)`
}

func (db *DB) SearchMessages(ctx context.Context, convKey string, terms []string, r search.Range, limit int) ([]store.MessageHit, error) {
	match := matchExpr(convKey, terms)
	if match == "" || limit <= 0 {
		return nil, nil
	}
	if r.After.IsZero() && r.Before.IsZero() {
		// Ranking the index alone saves joining every match; the mark already
		// keeps to the conversation, and the check below drops the rare rows of
		// other conversations whose text holds the mark.
		hits, err := db.rankedHits(ctx, `SELECT rowid, bm25(message_fts) FROM message_fts
			WHERE message_fts MATCH ? ORDER BY bm25(message_fts), rowid DESC LIMIT ?`, match, 2*limit)
		if err != nil {
			return nil, err
		}
		return db.inConversation(ctx, convKey, hits, limit)
	}
	query := `SELECT m.seq, bm25(message_fts) FROM message_fts JOIN messages m ON m.seq = message_fts.rowid
		WHERE message_fts MATCH ? AND m.conversation_key = ?`
	args := []any{match, convKey}
	if !r.After.IsZero() {
		query += ` AND m.occurred_at >= ?`
		args = append(args, fmtTime(r.After))
	}
	if !r.Before.IsZero() {
		query += ` AND m.occurred_at < ?`
		args = append(args, fmtTime(r.Before))
	}
	query += ` ORDER BY bm25(message_fts), m.seq DESC LIMIT ?`
	return db.rankedHits(ctx, query, append(args, limit)...)
}

// rankedHits runs query, which selects a seq and its bm25, into hits scored
// by -bm25.
func (db *DB) rankedHits(ctx context.Context, query string, args ...any) ([]store.MessageHit, error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []store.MessageHit
	for rows.Next() {
		var h store.MessageHit
		var bm25 float64
		if err := rows.Scan(&h.Seq, &bm25); err != nil {
			return nil, err
		}
		h.Score = -bm25
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// inConversation keeps the first limit of hits that are messages of convKey.
func (db *DB) inConversation(ctx context.Context, convKey string, hits []store.MessageHit, limit int) ([]store.MessageHit, error) {
	if len(hits) == 0 {
		return nil, nil
	}
	args := []any{convKey}
	for _, h := range hits {
		args = append(args, h.Seq)
	}
	rows, err := db.r.QueryContext(ctx, `SELECT seq FROM messages WHERE conversation_key = ? AND seq IN (`+
		strings.TrimSuffix(strings.Repeat("?, ", len(hits)), ", ")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ours := map[int64]bool{}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			return nil, err
		}
		ours[seq] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hits = slices.DeleteFunc(hits, func(h store.MessageHit) bool { return !ours[h.Seq] })
	return hits[:min(len(hits), limit)], nil
}

func (db *DB) MessagesAround(ctx context.Context, convKey string, fromSeq, toSeq int64, before, after int) ([]store.MessageRecord, error) {
	return db.queryMessages(ctx, `SELECT * FROM (
			SELECT `+messageCols+` FROM messages WHERE conversation_key = ? AND seq < ? ORDER BY seq DESC LIMIT ?)
		UNION ALL
		SELECT `+messageCols+` FROM messages WHERE conversation_key = ? AND seq BETWEEN ? AND ?
		UNION ALL
		SELECT * FROM (
			SELECT `+messageCols+` FROM messages WHERE conversation_key = ? AND seq > ? ORDER BY seq LIMIT ?)
		ORDER BY seq`,
		convKey, fromSeq, max(before, 0), convKey, fromSeq, toSeq, convKey, toSeq, max(after, 0))
}

func (db *DB) BackfillMessageIndex(ctx context.Context, batch int) (indexed, remaining int, err error) {
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		var mark int64
		if err := tx.QueryRowContext(ctx, `SELECT backfilled_seq FROM message_fts_progress WHERE id = 1`).Scan(&mark); err != nil {
			return err
		}
		type row struct {
			seq                 int64
			convKey, name, text string
		}
		var missing []row
		rows, err := tx.QueryContext(ctx, `SELECT seq, conversation_key, display_name, text FROM messages m
			WHERE seq > ? AND NOT EXISTS (SELECT 1 FROM message_fts f WHERE f.rowid = m.seq)
			ORDER BY seq LIMIT ?`, mark, max(batch, 1))
		if err != nil {
			return err
		}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.seq, &r.convKey, &r.name, &r.text); err != nil {
				_ = rows.Close()
				return err
			}
			missing = append(missing, r)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, r := range missing {
			if err := indexMessage(ctx, tx, r.seq, r.convKey, r.name, r.text); err != nil {
				return err
			}
		}
		if len(missing) == max(batch, 1) {
			mark = missing[len(missing)-1].seq
		} else if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), ?) FROM messages`, mark).Scan(&mark); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE message_fts_progress SET backfilled_seq = ? WHERE id = 1`, mark); err != nil {
			return err
		}
		indexed = len(missing)
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE seq > ?`, mark).Scan(&remaining)
	})
	if err != nil {
		return 0, 0, err
	}
	return indexed, remaining, nil
}
