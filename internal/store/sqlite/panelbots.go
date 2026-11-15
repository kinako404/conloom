package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/kinako404/conloom/internal/store"
)

func (db *DB) ListBotInstances(ctx context.Context) ([]store.BotInstance, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT id, platform, name FROM bot_instances ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.BotInstance
	for rows.Next() {
		var b store.BotInstance
		if err := rows.Scan(&b.ID, &b.Platform, &b.Name); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (db *DB) CreatePanelBot(ctx context.Context, b store.PanelBot) error {
	skills, err := marshalSkills(b.Skills)
	if err != nil {
		return err
	}
	created := fmtTime(nowIfZero(b.CreatedAt))
	updated := fmtTime(nowIfZero(b.UpdatedAt))
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO panel_bots
			(id, name, persona_template, skills, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.Name, b.PersonaTemplate, skills, b.CreatedBy, created, updated); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO bot_instances (id, platform, name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, b.ID, store.PlatformPanel, b.Name, created, updated)
		return err
	})
}

func (db *DB) UpdatePanelBot(ctx context.Context, b store.PanelBot) error {
	skills, err := marshalSkills(b.Skills)
	if err != nil {
		return err
	}
	updated := fmtTime(nowIfZero(b.UpdatedAt))
	return db.inTx(ctx, func(tx *sql.Tx) error {
		if err := affectedOne(tx.ExecContext(ctx, `UPDATE panel_bots
			SET name = ?, persona_template = ?, skills = ?, updated_at = ? WHERE id = ?`,
			b.Name, b.PersonaTemplate, skills, updated, b.ID)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE bot_instances SET name = ?, updated_at = ? WHERE id = ?`,
			b.Name, updated, b.ID)
		return err
	})
}

func (db *DB) ListPanelBots(ctx context.Context) ([]store.PanelBot, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT id, name, persona_template, skills, created_by, created_at, updated_at
		FROM panel_bots ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.PanelBot
	for rows.Next() {
		var b store.PanelBot
		var skills, created, updated string
		if err := rows.Scan(&b.ID, &b.Name, &b.PersonaTemplate, &skills, &b.CreatedBy, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(skills), &b.Skills); err != nil {
			return nil, err
		}
		if b.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if b.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (db *DB) DeletePanelBot(ctx context.Context, id string) error {
	return db.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM panel_bots WHERE id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return store.ErrNotFound
		}
		var hasData bool
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS (SELECT 1 FROM conversations WHERE bot_instance_id = ?1)
			OR EXISTS (SELECT 1 FROM tasks WHERE bot_instance_id = ?1)
			OR EXISTS (SELECT 1 FROM outbox_actions WHERE bot_instance_id = ?1)`, id).Scan(&hasData); err != nil {
			return err
		}
		if hasData {
			return store.ErrBotHasData
		}
		for _, table := range []string{"users", "events"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE bot_instance_id = ?`, id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM panel_bots WHERE id = ?`, id)
		return err
	})
}

// marshalSkills encodes skills as a JSON array, nil as [].
func marshalSkills(skills []string) (string, error) {
	if skills == nil {
		skills = []string{}
	}
	b, err := json.Marshal(skills)
	return string(b), err
}
