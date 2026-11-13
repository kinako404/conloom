package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// InsertModelCalls stores calls in one transaction; their IDs are ignored.
func (db *DB) InsertModelCalls(ctx context.Context, calls []store.ModelCall) error {
	if len(calls) == 0 {
		return nil
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO model_calls
			(at, bot_instance_id, conversation_key, task_id, purpose, role, model, prompt_tokens, completion_tokens,
			reasoning_tokens, estimated_tokens, latency_ms, outcome, finish_reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, c := range calls {
			if _, err := stmt.ExecContext(ctx, fmtTime(c.At), c.BotInstanceID, c.ConversationKey, c.TaskID, c.Purpose,
				c.Role, c.Model, c.PromptTokens, c.CompletionTokens, c.ReasoningTokens, c.EstimatedTokens, c.LatencyMS,
				c.Outcome, c.FinishReason); err != nil {
				return err
			}
		}
		return nil
	})
}

// slotSeconds is the width of a usage bucket. The model_calls_slot index is
// on unixepoch(at) / 900; the queries below repeat that expression verbatim
// so SQLite uses the index.
const slotSeconds = 900

// usageBucketsQuery and latencySamplesQuery take slotArgs.
const (
	usageBucketsQuery = `SELECT unixepoch(at) / 900, bot_instance_id, purpose, role, model,
		COUNT(*), SUM(outcome <> '` + store.OutcomeOK + `'), SUM(prompt_tokens), SUM(completion_tokens), SUM(reasoning_tokens)
		FROM model_calls WHERE unixepoch(at) / 900 BETWEEN ? AND ? AND at >= ? AND at < ?
		GROUP BY unixepoch(at) / 900, bot_instance_id, purpose, role, model
		ORDER BY unixepoch(at) / 900, bot_instance_id, purpose, role, model`
	latencySamplesQuery = `SELECT at, bot_instance_id, purpose, role, model, latency_ms
		FROM model_calls WHERE unixepoch(at) / 900 BETWEEN ? AND ? AND at >= ? AND at < ?
		AND outcome = '` + store.OutcomeOK + `'`
)

// slotArgs selects the calls with At in [from, to): the slot range lets
// SQLite seek the index, and the times trim it exactly.
func slotArgs(from, to time.Time) []any {
	return []any{from.Unix() / slotSeconds, to.Unix() / slotSeconds, fmtTime(from), fmtTime(to)}
}

// UsageBuckets sums the calls with At in [from, to) per 15-minute UTC slot
// and bot, purpose, role and model, ordered by those.
func (db *DB) UsageBuckets(ctx context.Context, from, to time.Time) ([]store.UsageBucket, error) {
	rows, err := db.r.QueryContext(ctx, usageBucketsQuery, slotArgs(from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.UsageBucket
	for rows.Next() {
		var b store.UsageBucket
		var slot int64
		if err := rows.Scan(&slot, &b.BotInstanceID, &b.Purpose, &b.Role, &b.Model, &b.Calls, &b.Errors,
			&b.PromptTokens, &b.CompletionTokens, &b.ReasoningTokens); err != nil {
			return nil, err
		}
		b.Start = time.Unix(slot*slotSeconds, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// LatencySamples returns the latencies of the successful calls with At in
// [from, to), in no particular order.
func (db *DB) LatencySamples(ctx context.Context, from, to time.Time) ([]store.LatencySample, error) {
	rows, err := db.r.QueryContext(ctx, latencySamplesQuery, slotArgs(from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.LatencySample
	for rows.Next() {
		var s store.LatencySample
		var at string
		if err := rows.Scan(&at, &s.BotInstanceID, &s.Purpose, &s.Role, &s.Model, &s.LatencyMS); err != nil {
			return nil, err
		}
		if s.At, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListModelCalls returns up to f.Limit calls matching f, newest ID first
// (none when f.Limit <= 0).
func (db *DB) ListModelCalls(ctx context.Context, f store.ModelCallFilter) ([]store.ModelCall, error) {
	if f.Limit <= 0 {
		return nil, nil
	}
	var where []string
	var args []any
	add := func(cond string, arg any) {
		where = append(where, cond)
		args = append(args, arg)
	}
	if !f.From.IsZero() {
		add("at >= ?", fmtTime(f.From))
	}
	if !f.To.IsZero() {
		add("at < ?", fmtTime(f.To))
	}
	if f.Model != "" {
		add("model = ?", f.Model)
	}
	if f.Purpose != "" {
		add("purpose = ?", f.Purpose)
	}
	if f.ConversationKey != "" {
		add("conversation_key = ?", f.ConversationKey)
	}
	if f.TaskID != "" {
		// The redundant task_id <> '' lets SQLite use the partial task index.
		add("task_id = ? AND task_id <> ''", f.TaskID)
	}
	if f.BeforeID > 0 {
		add("id < ?", f.BeforeID)
	}
	query := `SELECT id, at, bot_instance_id, conversation_key, task_id, purpose, role, model, prompt_tokens,
		completion_tokens, reasoning_tokens, estimated_tokens, latency_ms, outcome, finish_reason FROM model_calls`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := db.r.QueryContext(ctx, query+" ORDER BY id DESC LIMIT ?", append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ModelCall
	for rows.Next() {
		var c store.ModelCall
		var at string
		if err := rows.Scan(&c.ID, &at, &c.BotInstanceID, &c.ConversationKey, &c.TaskID, &c.Purpose, &c.Role, &c.Model,
			&c.PromptTokens, &c.CompletionTokens, &c.ReasoningTokens, &c.EstimatedTokens, &c.LatencyMS,
			&c.Outcome, &c.FinishReason); err != nil {
			return nil, err
		}
		if c.At, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *DB) PurgeModelCalls(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	return execCount(ctx, db.w, `DELETE FROM model_calls WHERE id IN (
		SELECT id FROM model_calls WHERE at < ? LIMIT ?)`, fmtTime(before), limit)
}
