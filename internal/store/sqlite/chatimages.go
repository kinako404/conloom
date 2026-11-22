package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// chatImageCols are the columns GetChatImages always reads.
const chatImageCols = `id, conversation_key, message_id, view_width, view_height, reply_width, reply_height,
	reply_tokens, description, seen, created_by, created_at`

func (db *DB) PutChatImage(ctx context.Context, img store.ChatImage) error {
	_, err := db.w.ExecContext(ctx, `INSERT INTO chat_images
		(id, conversation_key, message_id, view_jpeg, view_width, view_height, reply_jpeg, reply_width, reply_height,
			reply_tokens, description, seen, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		img.ID, img.ConversationKey, img.MessageID, blob(img.ViewJPEG), img.ViewWidth, img.ViewHeight,
		blob(img.ReplyJPEG), img.ReplyWidth, img.ReplyHeight, img.ReplyTokens, img.Description, img.Seen,
		img.CreatedBy, fmtTime(nowIfZero(img.CreatedAt)))
	return err
}

func (db *DB) GetChatImages(ctx context.Context, convKey string, ids []string, data store.ChatImageData) (map[string]store.ChatImage, error) {
	out := map[string]store.ChatImage{}
	if len(ids) == 0 {
		return out, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	cols := chatImageCols
	if data&store.ChatImageReply != 0 {
		cols += `, reply_jpeg`
	}
	if data&store.ChatImageView != 0 {
		cols += `, view_jpeg`
	}
	rows, err := db.r.QueryContext(ctx, `SELECT `+cols+` FROM chat_images
		WHERE conversation_key = ? AND id IN (SELECT value FROM json_each(?))`, convKey, string(list))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var img store.ChatImage
		var created string
		dest := []any{&img.ID, &img.ConversationKey, &img.MessageID, &img.ViewWidth, &img.ViewHeight, &img.ReplyWidth,
			&img.ReplyHeight, &img.ReplyTokens, &img.Description, &img.Seen, &img.CreatedBy, &created}
		if data&store.ChatImageReply != 0 {
			dest = append(dest, &img.ReplyJPEG)
		}
		if data&store.ChatImageView != 0 {
			dest = append(dest, &img.ViewJPEG)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if img.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out[img.ID] = img
	}
	return out, rows.Err()
}

func (db *DB) SetChatImageDescription(ctx context.Context, convKey, id, text string) error {
	n, err := execCount(ctx, db.w, `UPDATE chat_images SET description = ? WHERE conversation_key = ? AND id = ?`,
		text, convKey, id)
	if err == nil && n == 0 {
		return store.ErrNotFound
	}
	return err
}

func (db *DB) MarkChatImagesSeen(ctx context.Context, convKey string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = db.w.ExecContext(ctx, `UPDATE chat_images SET seen = 1
		WHERE conversation_key = ? AND seen = 0 AND id IN (SELECT value FROM json_each(?))`, convKey, string(list))
	return err
}

func (db *DB) PurgeChatImages(ctx context.Context, createdBefore time.Time) (int, error) {
	return execCount(ctx, db.w, `DELETE FROM chat_images WHERE message_id = '' AND created_at < ?`,
		fmtTime(createdBefore))
}

// claimChatImages marks the images m's image attachments name as carried by
// m. On a platform other than the panel, and for attachments without an ID,
// it does nothing. Every named image must be an unsent image of m's
// conversation, each named once; otherwise it fails and nothing is claimed.
func claimChatImages(ctx context.Context, tx *sql.Tx, m store.MessageRecord) error {
	if m.Platform != store.PlatformPanel {
		return nil
	}
	var ids []string
	for _, a := range m.Attachments {
		if a.Type == "image" && a.ID != "" {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	n, err := execCount(ctx, tx, `UPDATE chat_images SET message_id = ?
		WHERE conversation_key = ? AND message_id = '' AND id IN (SELECT value FROM json_each(?))`,
		m.ID, m.ConversationKey, string(list))
	if err != nil {
		return err
	}
	if n < len(ids) {
		return fmt.Errorf("ingest: message %s: %d of %d images not found unsent in %s", m.ID, len(ids)-n, len(ids), m.ConversationKey)
	}
	return nil
}
