package core

import (
	"context"
	"encoding/base64"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// maxTurnImages is the most chat images one reply sees: those of the newest
// trigger messages when they carry more.
const maxTurnImages = 4

const (
	// chatDescribeTimeout bounds all descriptions of the chat images of one
	// request.
	chatDescribeTimeout = 30 * time.Second
	// chatDescriptionMaxRunes is the most characters of a chat image's
	// description.
	chatDescriptionMaxRunes = 200
)

// chatDescribePrompt asks the vision model for a short factual description of
// a chat image, for a model that cannot see it.
const chatDescribePrompt = "这是聊天里对方发来的一张图片。请用一到两句话客观地描述图里有什么：主要的人或物、场景，以及能看清的文字要点。不猜测、不评价，看不清的不写。只输出描述本身，不要开场白。"

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

// blindImages returns in with every seen CurrentImages entry turned into text,
// for a profile that cannot take images: the cached description, else one the
// vision profile writes from the view copy (see chatDescription, cached with
// SetChatImageDescription), else Description "" (cannot see; Build adds the
// note). There is no description without a vision profile or, in a private
// chat, when the vision profile does not allow sensitive content. The
// descriptions of one call share chatDescribeTimeout; failures are logged and
// not cached, and the reply goes on.
func (o *Orchestrator) blindImages(ctx context.Context, task store.TaskRecord, in contextbuilder.Input) contextbuilder.Input {
	if len(in.CurrentImages) == 0 {
		return in
	}
	images := slices.Clone(in.CurrentImages)
	var need []string
	for i := range images {
		images[i].DataURL, images[i].Tokens = "", 0
		images[i].Description = chatDescription(images[i].Description)
		if images[i].Description == "" {
			need = append(need, images[i].ID)
		}
	}
	in.CurrentImages = images
	if len(need) == 0 {
		return in
	}
	notDescribed := func(id, role, reason string, latency time.Duration, err error) {
		attrs := []any{"task_id", task.ID, "image", id, "role", role, "latency_ms", latency.Milliseconds(), "reason", reason}
		if err != nil {
			attrs = append(attrs, "error", err)
		}
		o.log.Warn("chat image not described", attrs...)
	}
	p, ok := o.router.Profile(router.RoleVision)
	if !ok || convType(task.ConversationKey) == conloomapi.ConvPrivate && !p.Config.Privacy.AllowSensitiveContent {
		reason, role := "no vision profile", ""
		if ok {
			reason, role = "vision profile does not allow sensitive content", string(p.Role)
		}
		for _, id := range need {
			notDescribed(id, role, reason, 0, nil)
		}
		return in
	}
	role := string(p.Role)
	dctx, cancel := context.WithTimeout(ctx, o.chatDescribeTimeout)
	defer cancel()
	// A re-route reads the description an earlier attempt cached.
	found, err := o.st.GetChatImages(dctx, task.ConversationKey, need, store.ChatImageView)
	if err != nil {
		for _, id := range need {
			notDescribed(id, role, "load failed", 0, err)
		}
		return in
	}
	for i := range images {
		if images[i].Description != "" {
			continue
		}
		id := images[i].ID
		img := found[id]
		if text := chatDescription(img.Description); text != "" {
			images[i].Description = text
			continue
		}
		if len(img.ViewJPEG) == 0 {
			notDescribed(id, role, "no view copy", 0, nil)
			continue
		}
		start := time.Now()
		text, err := o.visionCall(dctx, p, "describe:"+id, chatDescribePrompt, img.ViewJPEG, img.ViewWidth, img.ViewHeight)
		latency := time.Since(start)
		text = chatDescription(text)
		switch {
		case err != nil:
			notDescribed(id, role, "vision call failed", latency, err)
			continue
		case text == "":
			notDescribed(id, role, "empty description", latency, nil)
			continue
		}
		images[i].Description = text
		if err := o.st.SetChatImageDescription(ctx, task.ConversationKey, id, text); err != nil {
			o.log.Warn("cache chat image description failed", "task_id", task.ID, "image", id, "error", err)
		}
		o.log.Info("chat image described", "task_id", task.ID, "image", id, "role", role,
			"latency_ms", latency.Milliseconds())
	}
	return in
}

// chatDescription is s as a chat image description: on one line, its white
// space runs as single spaces, square brackets made full-width so it cannot
// end its [图片：…] placeholder, cut to chatDescriptionMaxRunes characters.
func chatDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.NewReplacer("[", "［", "]", "］").Replace(s)
	if r := []rune(s); len(r) > chatDescriptionMaxRunes {
		s = strings.TrimSpace(string(r[:chatDescriptionMaxRunes]))
	}
	return s
}

// stateImageDescriptions returns, for a state step over turns, the newest
// turn's ImageDescriptions plus a description of every turn's seen chat image
// that has one: known when the turn was built, else cached since (a reply on
// a text-only profile writes them). A read failure is logged and leaves the
// images without one.
func (o *Orchestrator) stateImageDescriptions(ctx context.Context, turns []stateTurn) map[string]string {
	newest := turns[len(turns)-1]
	out := maps.Clone(newest.in.ImageDescriptions)
	add := func(id, text string) {
		if out == nil {
			out = map[string]string{}
		}
		out[id] = text
	}
	var missing []string
	for _, t := range turns {
		for _, img := range t.in.CurrentImages {
			if img.Description != "" {
				add(img.ID, img.Description)
			} else if !slices.Contains(missing, img.ID) {
				missing = append(missing, img.ID)
			}
		}
	}
	if len(missing) == 0 {
		return out
	}
	found, err := o.st.GetChatImages(ctx, newest.task.ConversationKey, missing, 0)
	if err != nil {
		o.log.Warn("load chat image descriptions failed", "task_id", newest.task.ID,
			"conversation", newest.task.ConversationKey, "error", err)
		return out
	}
	for _, id := range missing {
		if text := found[id].Description; text != "" {
			add(id, text)
		}
	}
	return out
}
