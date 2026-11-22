package core

import (
	"context"
	"encoding/base64"
	"slices"

	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// maxTurnImages is the most chat images one reply sees: those of the newest
// trigger messages when they carry more.
const maxTurnImages = 4

// unsentChatImages checks the image IDs of a panel message to convKey: at
// most conloomapi.MaxPanelMessageImages, distinct, and each an image uploaded
// to convKey and not sent yet; ErrBadImages otherwise.
func (o *Orchestrator) unsentChatImages(ctx context.Context, convKey string, images []string) error {
	if len(images) > conloomapi.MaxPanelMessageImages {
		return ErrBadImages
	}
	found, err := o.st.GetChatImages(ctx, convKey, images, 0)
	if err != nil {
		return err
	}
	for i, id := range images {
		if slices.Contains(images[:i], id) {
			return ErrBadImages
		}
		if img, ok := found[id]; !ok || img.MessageID != "" {
			return ErrBadImages
		}
	}
	return nil
}

// chatImageIDs returns the IDs of the chat images msgs carry, in message and
// attachment order.
func chatImageIDs(msgs []store.MessageRecord) []string {
	var out []string
	for _, m := range msgs {
		for _, a := range m.Attachments {
			if a.Type == "image" && a.ID != "" {
				out = append(out, a.ID)
			}
		}
	}
	return out
}

// turnImages returns, for a panel conversation, the reply copies of the image
// attachments of current (message order, the newest maxTurnImages) as seen
// images, and the cached descriptions of every other image attachment of
// history and current. An image that cannot be read is logged and left out
// (it renders [图片]). Other platforms: nil, nil.
func (o *Orchestrator) turnImages(ctx context.Context, task store.TaskRecord, conv store.ConversationRecord,
	history, current []store.MessageRecord) ([]contextbuilder.ChatImage, map[string]string) {
	if conv.Platform != store.PlatformPanel {
		return nil, nil
	}
	seen := chatImageIDs(current)
	other := chatImageIDs(history)
	if n := len(seen) - maxTurnImages; n > 0 {
		other = append(other, seen[:n]...)
		seen = seen[n:]
	}
	var images []contextbuilder.ChatImage
	if len(seen) > 0 {
		found, err := o.st.GetChatImages(ctx, conv.Key, seen, store.ChatImageReply)
		if err != nil {
			o.log.Warn("load chat images failed; replying without them", "task_id", task.ID,
				"conversation", conv.Key, "error", err)
			seen = nil
		}
		for _, id := range seen {
			img, ok := found[id]
			if !ok || len(img.ReplyJPEG) == 0 {
				o.log.Warn("chat image missing; replying without it", "task_id", task.ID,
					"conversation", conv.Key, "image", id)
				continue
			}
			images = append(images, contextbuilder.ChatImage{
				ID:          id,
				DataURL:     "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img.ReplyJPEG),
				Tokens:      img.ReplyTokens,
				Description: img.Description,
			})
		}
	}
	var descriptions map[string]string
	if len(other) > 0 {
		found, err := o.st.GetChatImages(ctx, conv.Key, other, 0)
		if err != nil {
			o.log.Warn("load chat image descriptions failed", "task_id", task.ID, "conversation", conv.Key, "error", err)
		}
		for id, img := range found {
			if img.Description == "" {
				continue
			}
			if descriptions == nil {
				descriptions = map[string]string{}
			}
			descriptions[id] = img.Description
		}
	}
	return images, descriptions
}
