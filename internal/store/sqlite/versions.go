package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/kinako404/conloom/internal/store"
)

// personaVersionCols reads a version's template, falling back to the
// conversation's first template for versions stored without one.
const personaVersionCols = `v.conversation_key, COALESCE(NULLIF(v.template_name, ''), i.template_name), v.version, v.persona,
	v.reason, v.updated_by, v.created_at`

const personaVersionFrom = ` FROM persona_versions v JOIN persona_instances i ON i.conversation_key = v.conversation_key`

func scanPersonaVersion(row scanner) (store.PersonaVersion, error) {
	var pv store.PersonaVersion
	var persona, created string
	err := row.Scan(&pv.ConversationKey, &pv.TemplateName, &pv.Version, &persona, &pv.Reason, &pv.UpdatedBy, &created)
	if err != nil {
		return pv, notFound(err)
	}
	if err := json.Unmarshal([]byte(persona), &pv.Persona); err != nil {
		return pv, err
	}
	pv.CreatedAt, err = parseTime(created)
	return pv, err
}

func (db *DB) LatestPersona(ctx context.Context, convKey string) (store.PersonaVersion, error) {
	return scanPersonaVersion(db.r.QueryRowContext(ctx, `SELECT `+personaVersionCols+personaVersionFrom+`
		WHERE v.conversation_key = ? ORDER BY v.version DESC LIMIT 1`, convKey))
}

func (db *DB) GetPersonaVersion(ctx context.Context, convKey string, version int) (store.PersonaVersion, error) {
	return scanPersonaVersion(db.r.QueryRowContext(ctx, `SELECT `+personaVersionCols+personaVersionFrom+`
		WHERE v.conversation_key = ? AND v.version = ?`, convKey, version))
}

// AppendPersonaVersion stores pv as the next version with its TemplateName;
// version 1 also creates the conversation's persona instance with it.
func (db *DB) AppendPersonaVersion(ctx context.Context, pv store.PersonaVersion) (store.PersonaVersion, error) {
	persona, err := json.Marshal(pv.Persona)
	if err != nil {
		return pv, err
	}
	pv.CreatedAt = nowIfZero(pv.CreatedAt).UTC()
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		version, err := nextVersion(ctx, tx, "persona_versions", pv.ConversationKey)
		if err != nil {
			return err
		}
		pv.Version = version
		if version == 1 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO persona_instances (conversation_key, template_name, created_at)
				VALUES (?, ?, ?)`, pv.ConversationKey, pv.TemplateName, fmtTime(pv.CreatedAt)); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO persona_versions
			(conversation_key, version, persona, reason, updated_by, created_at, template_name) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			pv.ConversationKey, pv.Version, string(persona), pv.Reason, pv.UpdatedBy, fmtTime(pv.CreatedAt), pv.TemplateName)
		return err
	})
	if err != nil {
		return store.PersonaVersion{}, err
	}
	return pv, nil
}

const policyVersionCols = `conversation_key, version, policy, reason, updated_by, created_at`

func scanPolicyVersion(row scanner) (store.PolicyVersion, error) {
	var pv store.PolicyVersion
	var policy, created string
	err := row.Scan(&pv.ConversationKey, &pv.Version, &policy, &pv.Reason, &pv.UpdatedBy, &created)
	if err != nil {
		return pv, notFound(err)
	}
	if err := json.Unmarshal([]byte(policy), &pv.Policy); err != nil {
		return pv, err
	}
	pv.CreatedAt, err = parseTime(created)
	return pv, err
}

func (db *DB) LatestPolicy(ctx context.Context, convKey string) (store.PolicyVersion, error) {
	return scanPolicyVersion(db.r.QueryRowContext(ctx, `SELECT `+policyVersionCols+` FROM group_policies
		WHERE conversation_key = ? ORDER BY version DESC LIMIT 1`, convKey))
}

func (db *DB) ListPolicyVersions(ctx context.Context, convKey string) ([]store.PolicyVersion, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+policyVersionCols+` FROM group_policies
		WHERE conversation_key = ? ORDER BY version DESC`, convKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.PolicyVersion
	for rows.Next() {
		pv, err := scanPolicyVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pv)
	}
	return out, rows.Err()
}

// AppendPolicyVersion stores pv as the next version.
func (db *DB) AppendPolicyVersion(ctx context.Context, pv store.PolicyVersion) (store.PolicyVersion, error) {
	policy, err := json.Marshal(pv.Policy)
	if err != nil {
		return pv, err
	}
	pv.CreatedAt = nowIfZero(pv.CreatedAt).UTC()
	err = db.inTx(ctx, func(tx *sql.Tx) error {
		version, err := nextVersion(ctx, tx, "group_policies", pv.ConversationKey)
		if err != nil {
			return err
		}
		pv.Version = version
		_, err = tx.ExecContext(ctx, `INSERT INTO group_policies
			(conversation_key, version, policy, reason, updated_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			pv.ConversationKey, pv.Version, string(policy), pv.Reason, pv.UpdatedBy, fmtTime(pv.CreatedAt))
		return err
	})
	if err != nil {
		return store.PolicyVersion{}, err
	}
	return pv, nil
}

// nextVersion returns the conversation's current maximum version in table plus one.
func nextVersion(ctx context.Context, tx *sql.Tx, table, convKey string) (int, error) {
	var current int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM `+table+` WHERE conversation_key = ?`,
		convKey).Scan(&current)
	return current + 1, err
}

func (db *DB) InsertAudit(ctx context.Context, a store.AuditEvent) error {
	_, err := db.w.ExecContext(ctx, `INSERT INTO audit_events (id, bot_instance_id, kind, actor, target, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.BotInstanceID, a.Kind, a.Actor, a.Target, a.Detail, fmtTime(nowIfZero(a.CreatedAt)))
	return err
}
