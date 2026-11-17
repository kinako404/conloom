package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

const policyDefaultsCols = `kind, version, policy, reason, updated_by, created_at`

func scanPolicyDefaults(row scanner) (store.PolicyDefaults, error) {
	var d store.PolicyDefaults
	var policy, created string
	err := row.Scan(&d.Kind, &d.Version, &policy, &d.Reason, &d.UpdatedBy, &created)
	if err != nil {
		return d, notFound(err)
	}
	if err := json.Unmarshal([]byte(policy), &d.Policy); err != nil {
		return d, err
	}
	d.CreatedAt, err = parseTime(created)
	return d, err
}

func (db *DB) LatestPolicyDefaults(ctx context.Context, kind string) (store.PolicyDefaults, error) {
	return scanPolicyDefaults(db.r.QueryRowContext(ctx, `SELECT `+policyDefaultsCols+` FROM policy_defaults
		WHERE kind = ? ORDER BY version DESC LIMIT 1`, kind))
}

func (db *DB) ListPolicyDefaults(ctx context.Context, kind string, limit int) ([]store.PolicyDefaults, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := db.r.QueryContext(ctx, `SELECT `+policyDefaultsCols+` FROM policy_defaults
		WHERE kind = ? ORDER BY version DESC LIMIT ?`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.PolicyDefaults
	for rows.Next() {
		d, err := scanPolicyDefaults(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AppendPolicyDefaults stores d as the next version of its kind.
func (db *DB) AppendPolicyDefaults(ctx context.Context, d store.PolicyDefaults) (store.PolicyDefaults, error) {
	d.CreatedAt = nowIfZero(d.CreatedAt).UTC()
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var current int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM policy_defaults WHERE kind = ?`,
			d.Kind).Scan(&current); err != nil {
			return err
		}
		d.Version = current + 1
		return insertPolicyDefaults(ctx, tx, d)
	})
	if err != nil {
		return store.PolicyDefaults{}, err
	}
	return d, nil
}

func insertPolicyDefaults(ctx context.Context, tx *sql.Tx, d store.PolicyDefaults) error {
	policy, err := json.Marshal(d.Policy)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO policy_defaults (`+policyDefaultsCols+`) VALUES (?, ?, ?, ?, ?, ?)`,
		d.Kind, d.Version, string(policy), d.Reason, d.UpdatedBy, fmtTime(d.CreatedAt))
	return err
}

func (db *DB) PolicyFollowers(ctx context.Context, convType string) ([]string, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT c.key FROM conversations c
		WHERE c.type = ? AND COALESCE((SELECT p.follows_defaults FROM group_policies p
			WHERE p.conversation_key = c.key ORDER BY p.version DESC LIMIT 1), 1) = 1
		ORDER BY c.key`, convType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// InitPolicyDefaults stores the first default policies in one transaction:
// each of imports, the config file's default policies, as version 1 of its
// kind, then each of builtins whose kind is not in imports and has no stored
// default policy as version 1 of its kind; builtins of a kind with one are
// skipped. When imports is not empty it records now under
// store.MetaConfigPolicyDefaultsMigrated. It returns the kinds of imports in
// their order. A row is stored as given, a zero CreatedAt as now. When a kind
// of imports already has a stored default policy it stores nothing and
// returns store.ErrAlreadyMigrated.
func (db *DB) InitPolicyDefaults(ctx context.Context, imports, builtins []store.PolicyDefaults, now time.Time) (imported []string, err error) {
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		exists := func(kind string) (bool, error) {
			var ok bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM policy_defaults WHERE kind = ?)`, kind).Scan(&ok)
			return ok, err
		}
		importing := map[string]bool{}
		for _, d := range imports {
			ok, err := exists(d.Kind)
			if err != nil {
				return err
			}
			if ok {
				return store.ErrAlreadyMigrated
			}
			importing[d.Kind] = true
		}
		first := func(d store.PolicyDefaults) store.PolicyDefaults {
			d.Version = 1
			if d.CreatedAt.IsZero() {
				d.CreatedAt = now
			}
			return d
		}
		for _, d := range imports {
			if err := insertPolicyDefaults(ctx, tx, first(d)); err != nil {
				return err
			}
			imported = append(imported, d.Kind)
		}
		for _, d := range builtins {
			if importing[d.Kind] {
				continue
			}
			ok, err := exists(d.Kind)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
			if err := insertPolicyDefaults(ctx, tx, first(d)); err != nil {
				return err
			}
		}
		if len(imports) == 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO app_meta (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			store.MetaConfigPolicyDefaultsMigrated, fmtTime(now), fmtTime(now))
		return err
	})
	if err != nil {
		return nil, err
	}
	return imported, nil
}
