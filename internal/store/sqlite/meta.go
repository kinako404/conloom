package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// GetMeta returns the database fact stored under key and whether there is
// one.
func (db *DB) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := db.r.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// MigrateConfigTemplates migrates the config file's persona templates rows
// into the database in one transaction: it stores each row whose name has no
// stored template, keeps the stored template of every other name, and
// records now under store.MetaConfigTemplatesMigrated. It returns the names
// it stored and those it skipped, both in the order of rows. A row is stored
// as given, a zero UpdatedAt as now and a zero CreatedAt as UpdatedAt. Once
// the fact is recorded it stores nothing and returns store.ErrAlreadyMigrated.
func (db *DB) MigrateConfigTemplates(ctx context.Context, rows []store.PersonaTemplateRecord, now time.Time) (imported, skipped []string, err error) {
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		var migrated bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM app_meta WHERE key = ?)`,
			store.MetaConfigTemplatesMigrated).Scan(&migrated); err != nil {
			return err
		}
		if migrated {
			return store.ErrAlreadyMigrated
		}
		for _, r := range rows {
			persona, err := json.Marshal(r.Persona)
			if err != nil {
				return err
			}
			updated := r.UpdatedAt
			if updated.IsZero() {
				updated = now
			}
			created := r.CreatedAt
			if created.IsZero() {
				created = updated
			}
			n, err := execCount(ctx, tx, `INSERT INTO persona_templates (name, persona, updated_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?) ON CONFLICT (name) DO NOTHING`,
				r.Name, string(persona), r.UpdatedBy, fmtTime(created), fmtTime(updated))
			if err != nil {
				return err
			}
			if n > 0 {
				imported = append(imported, r.Name)
			} else {
				skipped = append(skipped, r.Name)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO app_meta (key, value, updated_at) VALUES (?, ?, ?)`,
			store.MetaConfigTemplatesMigrated, fmtTime(now), fmtTime(now))
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return imported, skipped, nil
}
