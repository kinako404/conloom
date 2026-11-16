package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

const botSettingsColumns = `bot_instance_id, name, admins, private_chat, private_allowlist, enabled_groups,
	private_template, group_template, skills, updated_by, created_at, updated_at`

func (db *DB) GetBotSettings(ctx context.Context, id string) (store.BotSettings, error) {
	s, err := scanBotSettings(db.r.QueryRowContext(ctx, `SELECT `+botSettingsColumns+` FROM bot_settings
		WHERE bot_instance_id = ?`, id))
	return s, notFound(err)
}

func (db *DB) ListBotSettings(ctx context.Context) ([]store.BotSettings, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+botSettingsColumns+` FROM bot_settings ORDER BY bot_instance_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.BotSettings
	for rows.Next() {
		s, err := scanBotSettings(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (db *DB) PutBotSettings(ctx context.Context, s store.BotSettings) error {
	_, err := putBotSettings(ctx, db.w, s, nowIfZero(s.UpdatedAt), true)
	return err
}

// MigrateConfigBotSettings migrates the settings of the config file's bots
// rows into the database in one transaction: it stores each row whose bot has
// no stored settings, keeps the stored settings of every other bot, and
// records now under store.MetaConfigBotSettingsMigrated. It returns the IDs of
// the bots whose settings it stored, in the order of rows. A row is stored as
// PutBotSettings stores it, with now for a zero UpdatedAt. Once the fact is
// recorded it stores nothing and returns store.ErrAlreadyMigrated.
func (db *DB) MigrateConfigBotSettings(ctx context.Context, rows []store.BotSettings, now time.Time) (migrated []string, err error) {
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		var done bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM app_meta WHERE key = ?)`,
			store.MetaConfigBotSettingsMigrated).Scan(&done); err != nil {
			return err
		}
		if done {
			return store.ErrAlreadyMigrated
		}
		for _, r := range rows {
			updated := r.UpdatedAt
			if updated.IsZero() {
				updated = now
			}
			stored, err := putBotSettings(ctx, tx, r, updated, false)
			if err != nil {
				return err
			}
			if stored {
				migrated = append(migrated, r.BotInstanceID)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO app_meta (key, value, updated_at) VALUES (?, ?, ?)`,
			store.MetaConfigBotSettingsMigrated, fmtTime(now), fmtTime(now))
		return err
	})
	if err != nil {
		return nil, err
	}
	return migrated, nil
}

// putBotSettings stores s with updated as its UpdatedAt and reports whether
// it did: when replace is false and the bot has stored settings, it keeps
// them and stores nothing.
func putBotSettings(ctx context.Context, e execer, s store.BotSettings, updated time.Time, replace bool) (bool, error) {
	var lists [4]string
	for i, l := range [][]string{s.Admins, s.PrivateAllowlist, s.Groups, s.Skills} {
		v, err := marshalList(l)
		if err != nil {
			return false, err
		}
		lists[i] = v
	}
	created := s.CreatedAt
	if created.IsZero() {
		created = updated
	}
	conflict := `DO NOTHING`
	if replace {
		conflict = `DO UPDATE SET name = excluded.name, admins = excluded.admins, private_chat = excluded.private_chat,
			private_allowlist = excluded.private_allowlist, enabled_groups = excluded.enabled_groups,
			private_template = excluded.private_template, group_template = excluded.group_template,
			skills = excluded.skills, updated_by = excluded.updated_by, updated_at = excluded.updated_at`
	}
	n, err := execCount(ctx, e, `INSERT INTO bot_settings (`+botSettingsColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (bot_instance_id) `+conflict,
		s.BotInstanceID, s.Name, lists[0], s.PrivateChat, lists[1], lists[2],
		s.PrivateTemplate, s.GroupTemplate, lists[3], s.UpdatedBy, fmtTime(created), fmtTime(updated))
	return n > 0, err
}

func scanBotSettings(row scanner) (store.BotSettings, error) {
	var s store.BotSettings
	var admins, allowlist, groups, skills, created, updated string
	if err := row.Scan(&s.BotInstanceID, &s.Name, &admins, &s.PrivateChat, &allowlist, &groups,
		&s.PrivateTemplate, &s.GroupTemplate, &skills, &s.UpdatedBy, &created, &updated); err != nil {
		return store.BotSettings{}, err
	}
	for _, l := range []struct {
		name string
		raw  string
		dst  *[]string
	}{{"admins", admins, &s.Admins}, {"private_allowlist", allowlist, &s.PrivateAllowlist},
		{"enabled_groups", groups, &s.Groups}, {"skills", skills, &s.Skills}} {
		*l.dst = []string{}
		if err := json.Unmarshal([]byte(l.raw), l.dst); err != nil {
			return store.BotSettings{}, fmt.Errorf("bot settings of %s: %s: %w", s.BotInstanceID, l.name, err)
		}
	}
	var err error
	if s.CreatedAt, err = parseTime(created); err != nil {
		return store.BotSettings{}, err
	}
	if s.UpdatedAt, err = parseTime(updated); err != nil {
		return store.BotSettings{}, err
	}
	return s, nil
}
