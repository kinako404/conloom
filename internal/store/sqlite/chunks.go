package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// zeroTime is how a zero time.Time is stored; member_memory.forgotten_at holds
// it for members who were never forgotten.
var zeroTime = fmtTime(time.Time{})

const chunkCols = `id, conversation_key, start_seq, end_seq, start_at, end_at, text_hash, model, vector`

// MessagesUpTo returns the newest limit messages of the conversation with
// Seq <= seq, ordered by Seq ascending (none when limit <= 0).
func (db *DB) MessagesUpTo(ctx context.Context, convKey string, seq int64, limit int) ([]store.MessageRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	return db.queryMessages(ctx, `SELECT * FROM (
		SELECT `+messageCols+` FROM messages WHERE conversation_key = ? AND seq <= ? ORDER BY seq DESC LIMIT ?
	) ORDER BY seq`, convKey, seq, limit)
}

// ChunkState returns how far the conversation has been cut into chunks; the
// zero state when it has not been yet.
func (db *DB) ChunkState(ctx context.Context, convKey string) (store.ChunkState, error) {
	var s store.ChunkState
	err := db.r.QueryRowContext(ctx, `SELECT cursor_seq, model FROM memory_chunk_state WHERE conversation_key = ?`,
		convKey).Scan(&s.CursorSeq, &s.Model)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ChunkState{}, nil
	}
	return s, err
}

// ForgetStamp identifies the forgetting done in the conversation so far: it
// changes whenever a member of it is forgotten.
func (db *DB) ForgetStamp(ctx context.Context, convKey string) (string, error) {
	return forgetStamp(ctx, db.r, convKey)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func forgetStamp(ctx context.Context, q queryer, convKey string) (string, error) {
	var s string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(group_concat(platform_user_id || '@' || forgotten_at, ','), '') FROM (
		SELECT platform_user_id, forgotten_at FROM member_memory
		WHERE conversation_key = ? AND forgotten_at > ? ORDER BY platform_user_id)`, convKey, zeroTime).Scan(&s)
	return s, err
}

// WriteChunks applies w and reports whether it did: nothing is written when
// the conversation's forget stamp is no longer w.ForgetStamp.
func (db *DB) WriteChunks(ctx context.Context, w store.ChunkWrite) (bool, error) {
	written := false
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		stamp, err := forgetStamp(ctx, tx, w.ConversationKey)
		if err != nil {
			return err
		}
		if stamp != w.ForgetStamp {
			return nil
		}
		for _, r := range w.Rebuilt {
			if _, err := tx.ExecContext(ctx, `DELETE FROM memory_chunks
				WHERE conversation_key = ? AND model = ? AND start_seq >= ? AND end_seq <= ?`,
				w.ConversationKey, w.Model, r.StartSeq, r.EndSeq); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM memory_chunk_rebuild WHERE id = ? AND conversation_key = ?`,
				r.ID, w.ConversationKey); err != nil {
				return err
			}
		}
		now := fmtTime(time.Now())
		for _, c := range w.Chunks {
			if c.ConversationKey != w.ConversationKey || c.Model != w.Model {
				return fmt.Errorf("chunk write for %s/%s: chunk of %s/%s", w.ConversationKey, w.Model, c.ConversationKey, c.Model)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO memory_chunks
				(conversation_key, start_seq, end_seq, start_at, end_at, text_hash, model, dims, vector, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.ConversationKey, c.StartSeq, c.EndSeq, fmtTime(c.StartAt), fmtTime(c.EndAt), c.TextHash, c.Model,
				len(c.Vector), encodeVector(c.Vector), now); err != nil {
				return err
			}
		}
		if w.CursorSeq > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO memory_chunk_state (conversation_key, cursor_seq, model) VALUES (?, ?, ?)
				ON CONFLICT (conversation_key) DO UPDATE SET cursor_seq = MAX(cursor_seq, excluded.cursor_seq)`,
				w.ConversationKey, w.CursorSeq, w.Model); err != nil {
				return err
			}
		}
		written = true
		return nil
	})
	return written && err == nil, err
}

// SwitchChunkModel makes model the conversation's embedding model: when it
// was another, every chunk of another model is queued to be rebuilt.
func (db *DB) SwitchChunkModel(ctx context.Context, convKey, model string) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_chunk_rebuild (conversation_key, start_seq, end_seq)
			SELECT conversation_key, start_seq, end_seq FROM memory_chunks WHERE conversation_key = ? AND model <> ?
			ORDER BY start_seq`, convKey, model); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO memory_chunk_state (conversation_key, cursor_seq, model) VALUES (?, 0, ?)
			ON CONFLICT (conversation_key) DO UPDATE SET model = excluded.model`, convKey, model)
		return err
	})
}

// ChunkRebuilds returns up to limit of the conversation's queued rebuilds,
// the newest stretch first.
func (db *DB) ChunkRebuilds(ctx context.Context, convKey string, limit int) ([]store.ChunkRebuild, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT id, start_seq, end_seq FROM memory_chunk_rebuild
		WHERE conversation_key = ? ORDER BY end_seq DESC, id LIMIT ?`, convKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ChunkRebuild
	for rows.Next() {
		var r store.ChunkRebuild
		if err := rows.Scan(&r.ID, &r.StartSeq, &r.EndSeq); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountChunkRebuilds counts the queued rebuilds of all conversations.
func (db *DB) CountChunkRebuilds(ctx context.Context) (int, error) {
	var n int
	err := db.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_chunk_rebuild`).Scan(&n)
	return n, err
}

// DeleteStaleChunks deletes the conversation's chunks of models other than
// model once it has no rebuilds queued, and returns how many it deleted.
func (db *DB) DeleteStaleChunks(ctx context.Context, convKey, model string) (int, error) {
	res, err := db.w.ExecContext(ctx, `DELETE FROM memory_chunks WHERE conversation_key = ? AND model <> ?
		AND NOT EXISTS (SELECT 1 FROM memory_chunk_rebuild WHERE conversation_key = ?)`, convKey, model, convKey)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// ChunkStamp summarises the conversation's chunks of model; see
// store.ChunkStamp.
func (db *DB) ChunkStamp(ctx context.Context, convKey, model string, upToID int64) (store.ChunkStamp, error) {
	var s store.ChunkStamp
	err := db.r.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(id), 0), COALESCE(SUM(id <= ?), 0)
		FROM memory_chunks WHERE conversation_key = ? AND model = ?`, upToID, convKey, model).Scan(&s.Count, &s.MaxID, &s.UpTo)
	return s, err
}

// LoadChunks returns the conversation's chunks of model with an ID above
// afterID, in ID order.
func (db *DB) LoadChunks(ctx context.Context, convKey, model string, afterID int64) ([]store.MemoryChunk, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+chunkCols+` FROM memory_chunks
		WHERE conversation_key = ? AND model = ? AND id > ? ORDER BY id`, convKey, model, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.MemoryChunk
	for rows.Next() {
		var c store.MemoryChunk
		var startAt, endAt string
		var blob []byte
		if err := rows.Scan(&c.ID, &c.ConversationKey, &c.StartSeq, &c.EndSeq, &startAt, &endAt, &c.TextHash, &c.Model, &blob); err != nil {
			return nil, err
		}
		if c.StartAt, err = parseTime(startAt); err != nil {
			return nil, err
		}
		if c.EndAt, err = parseTime(endAt); err != nil {
			return nil, err
		}
		if c.Vector, err = decodeVector(blob); err != nil {
			return nil, fmt.Errorf("chunk %d: %w", c.ID, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReconcileForgottenChunks applies forgetting to the chunks for every
// forgotten member whose forgetting they have not seen yet (a binary without
// semantic search forgot them), as ForgetMember or ForgetPrivate would have,
// and returns how many members it handled. Running it again changes nothing.
func (db *DB) ReconcileForgottenChunks(ctx context.Context) (int, error) {
	n := 0
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT m.conversation_key, m.platform_user_id, m.forgotten_at FROM member_memory m
			LEFT JOIN memory_chunk_forgets f ON f.conversation_key = m.conversation_key AND f.platform_user_id = m.platform_user_id
			WHERE m.forgotten_at > ? AND (f.forgotten_at IS NULL OR f.forgotten_at <> m.forgotten_at)
			ORDER BY m.conversation_key, m.platform_user_id`, zeroTime)
		if err != nil {
			return err
		}
		type forgotten struct{ convKey, userID, at string }
		var todo []forgotten
		for rows.Next() {
			var f forgotten
			if err := rows.Scan(&f.convKey, &f.userID, &f.at); err != nil {
				rows.Close()
				return err
			}
			todo = append(todo, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, f := range todo {
			at, err := parseTime(f.at)
			if err != nil {
				return err
			}
			if parts := strings.SplitN(f.convKey, ":", 4); len(parts) == 4 && parts[2] == conloomapi.ConvPrivate {
				err = forgetPrivateChunks(ctx, tx, f.convKey, f.userID, at)
			} else {
				err = forgetMemberChunks(ctx, tx, f.convKey, f.userID, at)
			}
			if err != nil {
				return err
			}
		}
		n = len(todo)
		return nil
	})
	return n, err
}

// forgetMemberChunks deletes the conversation's chunks holding a message the
// user sent before at and queues their stretches to be rebuilt without it.
func forgetMemberChunks(ctx context.Context, tx *sql.Tx, convKey, userID string, at time.Time) error {
	match := `c.conversation_key = ? AND EXISTS (SELECT 1 FROM messages m
		WHERE m.conversation_key = c.conversation_key AND m.seq BETWEEN c.start_seq AND c.end_seq
		AND m.platform_user_id = ? AND m.from_bot = 0 AND m.occurred_at < ?)`
	args := []any{convKey, userID, fmtTime(at)}
	if err := dropChunks(ctx, tx, match, args, match, args); err != nil {
		return err
	}
	return chunksForgot(ctx, tx, convKey, userID, at)
}

// forgetPrivateChunks deletes the conversation's chunks that start before at
// and queues those running past it to be rebuilt from what is left.
func forgetPrivateChunks(ctx context.Context, tx *sql.Tx, convKey, userID string, at time.Time) error {
	t := fmtTime(at)
	if err := dropChunks(ctx, tx, `c.conversation_key = ? AND c.start_at < ?`, []any{convKey, t},
		`c.conversation_key = ? AND c.start_at < ? AND c.end_at >= ?`, []any{convKey, t, t}); err != nil {
		return err
	}
	return chunksForgot(ctx, tx, convKey, userID, at)
}

// chunksForgot records that the chunks have seen the user forgotten at at.
func chunksForgot(ctx context.Context, tx *sql.Tx, convKey, userID string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_chunk_forgets (conversation_key, platform_user_id, forgotten_at)
		VALUES (?, ?, ?) ON CONFLICT (conversation_key, platform_user_id) DO UPDATE SET forgotten_at = excluded.forgotten_at`,
		convKey, userID, fmtTime(at))
	return err
}

// dropChunks queues the stretches of the chunks c matching rebuild, then
// deletes the chunks matching drop.
func dropChunks(ctx context.Context, tx *sql.Tx, drop string, dropArgs []any, rebuild string, rebuildArgs []any) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_chunk_rebuild (conversation_key, start_seq, end_seq)
		SELECT c.conversation_key, c.start_seq, c.end_seq FROM memory_chunks c WHERE `+rebuild+`
		ORDER BY c.start_seq`, rebuildArgs...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM memory_chunks WHERE id IN (SELECT c.id FROM memory_chunks c WHERE `+drop+`)`, dropArgs...)
	return err
}

func encodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func decodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector of %d bytes", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
