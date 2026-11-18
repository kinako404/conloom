package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const sceneCols = `conversation_key, mode, place, since_at, touched_at, prev_mode, prev_place, updated_at, origin_conversation_key, switch_task_id`

func (db *DB) GetScene(ctx context.Context, convKey string) (store.ConversationScene, error) {
	var s store.ConversationScene
	var since, touched, updated string
	err := db.r.QueryRowContext(ctx, `SELECT `+sceneCols+` FROM conversation_scene WHERE conversation_key = ?`, convKey).
		Scan(&s.ConversationKey, &s.Mode, &s.Place, &since, &touched, &s.PrevMode, &s.PrevPlace, &updated,
			&s.OriginConversationKey, &s.SwitchTaskID)
	if err != nil {
		return store.ConversationScene{}, notFound(err)
	}
	if s.SinceAt, err = parseTime(since); err != nil {
		return store.ConversationScene{}, err
	}
	if s.TouchedAt, err = parseTime(touched); err != nil {
		return store.ConversationScene{}, err
	}
	if s.UpdatedAt, err = parseTime(updated); err != nil {
		return store.ConversationScene{}, err
	}
	return s, nil
}

func (db *DB) SetScene(ctx context.Context, s store.ConversationScene) (bool, error) {
	if s.ConversationKey == "" {
		return false, errors.New("set scene: empty conversation key")
	}
	if s.Mode != conloomapi.SceneOnline && s.Mode != conloomapi.SceneInPerson {
		return false, fmt.Errorf("set scene of %s: unknown mode %q", s.ConversationKey, s.Mode)
	}
	now := time.Now()
	for _, t := range []*time.Time{&s.SinceAt, &s.TouchedAt, &s.UpdatedAt} {
		if t.IsZero() {
			*t = now
		}
	}
	var changed bool
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		changed = false
		mode, place, found, err := storedScene(ctx, tx, s.ConversationKey)
		if err != nil {
			return err
		}
		if mode == s.Mode && place == s.Place {
			if !found {
				return nil
			}
			_, err := tx.ExecContext(ctx, `UPDATE conversation_scene SET touched_at = ?, updated_at = ?,
				origin_conversation_key = ?, switch_task_id = CASE WHEN switch_task_id = ? THEN switch_task_id ELSE '' END
				WHERE conversation_key = ?`,
				fmtTime(s.TouchedAt), fmtTime(s.UpdatedAt), s.OriginConversationKey, s.SwitchTaskID, s.ConversationKey)
			return err
		}
		changed = true
		_, err = tx.ExecContext(ctx, `INSERT INTO conversation_scene (`+sceneCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (conversation_key) DO UPDATE SET mode = excluded.mode, place = excluded.place,
				since_at = excluded.since_at, touched_at = excluded.touched_at, prev_mode = excluded.prev_mode,
				prev_place = excluded.prev_place, updated_at = excluded.updated_at,
				origin_conversation_key = excluded.origin_conversation_key, switch_task_id = excluded.switch_task_id`,
			s.ConversationKey, s.Mode, s.Place, fmtTime(s.SinceAt), fmtTime(s.TouchedAt), mode, place,
			fmtTime(s.UpdatedAt), s.OriginConversationKey, s.SwitchTaskID)
		return err
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// storedScene returns the mode and place of convKey's scene, online and ""
// with found false without one.
func storedScene(ctx context.Context, q queryer, convKey string) (mode, place string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT mode, place FROM conversation_scene WHERE conversation_key = ?`, convKey).
		Scan(&mode, &place)
	if errors.Is(err, sql.ErrNoRows) {
		return conloomapi.SceneOnline, "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return mode, place, true, nil
}

func (db *DB) TouchScene(ctx context.Context, convKey string, at time.Time) (bool, error) {
	var n int
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = execCount(ctx, tx, `UPDATE conversation_scene SET touched_at = ?
			WHERE conversation_key = ? AND mode = ? AND touched_at < ?`,
			fmtTime(at), convKey, conloomapi.SceneInPerson, fmtTime(at))
		return err
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// EndIdleScene stores now as the scene's UpdatedAt.
func (db *DB) EndIdleScene(ctx context.Context, convKey string, touchedAt, since time.Time) (bool, error) {
	var n int
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = execCount(ctx, tx, `UPDATE conversation_scene SET mode = ?, place = '', since_at = ?,
				prev_mode = mode, prev_place = place, switch_task_id = '', updated_at = ?
			WHERE conversation_key = ? AND mode = ? AND touched_at = ?`,
			conloomapi.SceneOnline, fmtTime(since), fmtTime(time.Now()), convKey, conloomapi.SceneInPerson, fmtTime(touchedAt))
		return err
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
