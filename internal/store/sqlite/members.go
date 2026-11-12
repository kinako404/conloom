package sqlite

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// GetMember returns the member row; Interests is never nil.
func (db *DB) GetMember(ctx context.Context, convKey, userID string) (store.MemberRecord, error) {
	m := store.MemberRecord{ConversationKey: convKey, PlatformUserID: userID}
	var engaged, interests, updated string
	err := db.r.QueryRowContext(ctx, `SELECT last_engaged_at, proactive_level, level_set_by_user, interests, updated_at
		FROM conversation_members WHERE conversation_key = ? AND platform_user_id = ?`, convKey, userID).
		Scan(&engaged, &m.ProactiveLevel, &m.LevelSetByUser, &interests, &updated)
	if err != nil {
		return store.MemberRecord{}, notFound(err)
	}
	if err := json.Unmarshal([]byte(interests), &m.Interests); err != nil {
		return store.MemberRecord{}, err
	}
	if m.Interests == nil {
		m.Interests = map[string]float64{}
	}
	if m.LastEngagedAt, err = parseTime(engaged); err != nil {
		return store.MemberRecord{}, err
	}
	if m.UpdatedAt, err = parseTime(updated); err != nil {
		return store.MemberRecord{}, err
	}
	return m, nil
}

// PutMember stores m, replacing any row with the same key; a zero UpdatedAt
// is stored as now.
func (db *DB) PutMember(ctx context.Context, m store.MemberRecord) error {
	interests := m.Interests
	if interests == nil {
		interests = map[string]float64{}
	}
	body, err := json.Marshal(interests)
	if err != nil {
		return err
	}
	_, err = db.w.ExecContext(ctx, `INSERT INTO conversation_members
		(conversation_key, platform_user_id, last_engaged_at, proactive_level, level_set_by_user, interests, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (conversation_key, platform_user_id) DO UPDATE SET
			last_engaged_at = excluded.last_engaged_at, proactive_level = excluded.proactive_level,
			level_set_by_user = excluded.level_set_by_user, interests = excluded.interests,
			updated_at = excluded.updated_at`,
		m.ConversationKey, m.PlatformUserID, fmtTime(m.LastEngagedAt), m.ProactiveLevel, m.LevelSetByUser,
		string(body), fmtTime(nowIfZero(m.UpdatedAt)))
	return err
}

// AppendProactiveLog stores e; a zero CreatedAt is stored as now.
func (db *DB) AppendProactiveLog(ctx context.Context, e store.ProactiveLogEntry) error {
	_, err := db.w.ExecContext(ctx, `INSERT INTO proactive_log
		(conversation_key, platform_user_id, kind, task_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.ConversationKey, e.PlatformUserID, e.Kind, e.TaskID, fmtTime(nowIfZero(e.CreatedAt)))
	return err
}

func (db *DB) CountProactive(ctx context.Context, convKey, userID, kind string, since time.Time) (int, error) {
	q := `SELECT COUNT(*) FROM proactive_log WHERE conversation_key = ? AND kind = ? AND created_at >= ?`
	args := []any{convKey, kind, fmtTime(since)}
	if userID != "" {
		q += ` AND platform_user_id = ?`
		args = append(args, userID)
	}
	var n int
	err := db.r.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

func (db *DB) PurgeProactiveLog(ctx context.Context, before time.Time) (int, error) {
	res, err := db.w.ExecContext(ctx, `DELETE FROM proactive_log WHERE created_at < ?`, fmtTime(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
