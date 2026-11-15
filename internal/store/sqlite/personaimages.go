package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// personaImageCols are the columns GetPersonaImage always reads.
const personaImageCols = `id, mime, width, height, reply_jpeg, reply_width, reply_height, reply_tokens, created_by, created_at`

func (db *DB) PutPersonaImage(ctx context.Context, img store.PersonaImage) error {
	_, err := db.w.ExecContext(ctx, `INSERT INTO persona_images
		(id, mime, width, height, original, reply_jpeg, reply_width, reply_height, reply_tokens, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET created_at = excluded.created_at, created_by = excluded.created_by`,
		img.ID, img.MIME, img.Width, img.Height, blob(img.Original), blob(img.ReplyJPEG),
		img.ReplyWidth, img.ReplyHeight, img.ReplyTokens, img.CreatedBy, fmtTime(nowIfZero(img.CreatedAt)))
	return err
}

func (db *DB) GetPersonaImage(ctx context.Context, id string, original bool) (store.PersonaImage, error) {
	var img store.PersonaImage
	var created string
	cols := personaImageCols
	dest := []any{&img.ID, &img.MIME, &img.Width, &img.Height, &img.ReplyJPEG, &img.ReplyWidth, &img.ReplyHeight,
		&img.ReplyTokens, &img.CreatedBy, &created}
	if original {
		cols += `, original`
		dest = append(dest, &img.Original)
	}
	if err := db.r.QueryRowContext(ctx, `SELECT `+cols+` FROM persona_images WHERE id = ?`, id).Scan(dest...); err != nil {
		return store.PersonaImage{}, notFound(err)
	}
	var err error
	img.CreatedAt, err = parseTime(created)
	return img, err
}

func (db *DB) PersonaImageExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := db.r.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM persona_images WHERE id = ?)`, id).Scan(&ok)
	return ok, err
}

func (db *DB) PurgePersonaImages(ctx context.Context, createdBefore time.Time, keep []string) (int, error) {
	if keep == nil {
		keep = []string{}
	}
	kept, err := json.Marshal(keep)
	if err != nil {
		return 0, err
	}
	return execCount(ctx, db.w, `DELETE FROM persona_images WHERE created_at < ? AND id NOT IN (
		SELECT value FROM json_each(?)
		UNION
		SELECT image FROM (
			SELECT `+personaImageRef+` AS image FROM persona_templates
			UNION ALL
			SELECT `+personaImageRef+` FROM persona_versions)
		WHERE image IS NOT NULL)`, fmtTime(createdBefore), string(kept))
}

// personaImageRef is the appearance image of a row's persona, NULL when it has
// none or the persona is not valid JSON: one malformed row must not make every
// purge fail. CASE, unlike AND, is evaluated in order.
const personaImageRef = `CASE WHEN json_valid(persona) THEN json_extract(persona, '$.appearance_image') END`

func (db *DB) ListPersonaTemplates(ctx context.Context) ([]store.PersonaTemplateRecord, []store.UnreadablePersonaTemplate, error) {
	rows, err := db.r.QueryContext(ctx, `SELECT name, persona, updated_by, created_at, updated_at
		FROM persona_templates ORDER BY name`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []store.PersonaTemplateRecord
	var unreadable []store.UnreadablePersonaTemplate
	for rows.Next() {
		var r store.PersonaTemplateRecord
		var persona, created, updated string
		if err := rows.Scan(&r.Name, &persona, &r.UpdatedBy, &created, &updated); err != nil {
			return nil, nil, err
		}
		if err := decodeTemplate(&r, persona, created, updated); err != nil {
			unreadable = append(unreadable, store.UnreadablePersonaTemplate{Name: r.Name, Err: err})
			continue
		}
		out = append(out, r)
	}
	return out, unreadable, rows.Err()
}

// decodeTemplate sets r's persona and times from their stored text.
func decodeTemplate(r *store.PersonaTemplateRecord, persona, created, updated string) error {
	if err := json.Unmarshal([]byte(persona), &r.Persona); err != nil {
		return fmt.Errorf("persona: %w", err)
	}
	var err error
	if r.CreatedAt, err = parseTime(created); err != nil {
		return fmt.Errorf("created_at: %w", err)
	}
	if r.UpdatedAt, err = parseTime(updated); err != nil {
		return fmt.Errorf("updated_at: %w", err)
	}
	return nil
}

func (db *DB) PutPersonaTemplate(ctx context.Context, r store.PersonaTemplateRecord) error {
	persona, err := json.Marshal(r.Persona)
	if err != nil {
		return err
	}
	updated := nowIfZero(r.UpdatedAt)
	created := r.CreatedAt
	if created.IsZero() {
		created = updated
	}
	_, err = db.w.ExecContext(ctx, `INSERT INTO persona_templates (name, persona, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET persona = excluded.persona, updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`,
		r.Name, string(persona), r.UpdatedBy, fmtTime(created), fmtTime(updated))
	return err
}

func (db *DB) DeletePersonaTemplate(ctx context.Context, name string) (bool, error) {
	n, err := execCount(ctx, db.w, `DELETE FROM persona_templates WHERE name = ?`, name)
	return n > 0, err
}

// blob returns b, or an empty slice for nil, which would be stored as NULL.
func blob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
