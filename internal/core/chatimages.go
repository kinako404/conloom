package core

import (
	"context"
	"encoding/base64"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/admission"
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
	// request, or of one reply's seen images in the background.
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

// chatImagesTaken reports whether one of images is no longer an image of
// convKey, or is carried by a message other than messageID. A read error is
// not reported as taken.
func (o *Orchestrator) chatImagesTaken(ctx context.Context, convKey string, images []string, messageID string) bool {
	if len(images) == 0 {
		return false
	}
	found, err := o.st.GetChatImages(ctx, convKey, images, 0)
	if err != nil {
		return false
	}
	for _, id := range images {
		if img, ok := found[id]; !ok || img.MessageID != "" && img.MessageID != messageID {
			return true
		}
	}
	return false
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
// images, the cached descriptions of every other image attachment of history
// and current, and which of those others without one a reply model took
// itself in an earlier turn (see markSeen). An image that cannot be read is
// logged and left out (it renders [图片]). Other platforms: nil, nil, nil.
func (o *Orchestrator) turnImages(ctx context.Context, task store.TaskRecord, conv store.ConversationRecord,
	history, current []store.MessageRecord) ([]contextbuilder.ChatImage, map[string]string, map[string]bool) {
	if conv.Platform != store.PlatformPanel {
		return nil, nil, nil
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
	var seenEarlier map[string]bool
	if len(other) > 0 {
		found, err := o.st.GetChatImages(ctx, conv.Key, other, 0)
		if err != nil {
			o.log.Warn("load chat image descriptions failed", "task_id", task.ID, "conversation", conv.Key, "error", err)
		}
		for id, img := range found {
			switch {
			case img.Description != "":
				if descriptions == nil {
					descriptions = map[string]string{}
				}
				descriptions[id] = img.Description
			case img.Seen:
				if seenEarlier == nil {
					seenEarlier = map[string]bool{}
				}
				seenEarlier[id] = true
			}
		}
	}
	return images, descriptions, seenEarlier
}

// blindImages returns in with every seen CurrentImages entry turned into text,
// for a profile that cannot take images: the cached description, else one the
// vision profile writes (see describeChatImages), else Description "" (cannot
// see; Build adds the note). The reply goes on whatever the outcome.
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
	described := o.describeChatImages(ctx, task, need, admission.PriorityInteractive)
	for i := range images {
		if images[i].Description == "" {
			images[i].Description = described[images[i].ID]
		}
	}
	return in
}

// describeChatImages returns descriptions, by ID, of the chat images ids of
// task's conversation: the cached one, else one the vision profile writes from
// the view copy (see chatDescription), leased at priority and cached with
// SetChatImageDescription. There is none without a vision profile or, in a
// private chat, when the vision profile does not allow sensitive content
// (logged at Info). All descriptions of one call share chatDescribeTimeout;
// once it passed, or ctx ended, the images left are not sent to the vision
// profile (one log line). Failures are logged and not cached.
func (o *Orchestrator) describeChatImages(ctx context.Context, task store.TaskRecord, ids []string,
	priority admission.Priority) map[string]string {
	notDescribed := func(level slog.Level, ids []string, role, reason string, latency time.Duration, err error) {
		attrs := []any{"task_id", task.ID, "conversation", task.ConversationKey, "role", role,
			"latency_ms", latency.Milliseconds(), "reason", reason}
		if len(ids) == 1 {
			attrs = append(attrs, "image", ids[0])
		} else {
			attrs = append(attrs, "images", ids)
		}
		if err != nil {
			attrs = append(attrs, "error", err)
		}
		o.log.Log(context.Background(), level, "chat image not described", attrs...)
	}
	p, ok := o.router.Profile(router.RoleVision)
	if !ok {
		notDescribed(slog.LevelInfo, ids, "", "no vision profile", 0, nil)
		return nil
	}
	role := string(p.Role)
	if convType(task.ConversationKey) == conloomapi.ConvPrivate && !p.Config.Privacy.AllowSensitiveContent {
		notDescribed(slog.LevelInfo, ids, role, "vision profile does not allow sensitive content", 0, nil)
		return nil
	}
	dctx, cancel := context.WithTimeout(ctx, o.chatDescribeTimeout)
	defer cancel()
	// A re-route reads the description an earlier attempt cached.
	found, err := o.st.GetChatImages(dctx, task.ConversationKey, ids, store.ChatImageView)
	if err != nil {
		notDescribed(slog.LevelWarn, ids, role, "load failed", 0, err)
		return nil
	}
	out := map[string]string{}
	var late []string
	for _, id := range ids {
		img := found[id]
		if text := chatDescription(img.Description); text != "" {
			out[id] = text
			continue
		}
		if len(img.ViewJPEG) == 0 {
			notDescribed(slog.LevelWarn, []string{id}, role, "no view copy", 0, nil)
			continue
		}
		if dctx.Err() != nil {
			late = append(late, id)
			continue
		}
		start := time.Now()
		text, err := o.visionCall(dctx, p, task, priority, "describe:"+id, chatDescribePrompt, img.ViewJPEG, img.ViewWidth, img.ViewHeight)
		latency := time.Since(start)
		text = chatDescription(text)
		switch {
		case err != nil:
			notDescribed(slog.LevelWarn, []string{id}, role, "vision call failed", latency, err)
			continue
		case text == "":
			notDescribed(slog.LevelWarn, []string{id}, role, "empty description", latency, nil)
			continue
		}
		out[id] = text
		if err := o.st.SetChatImageDescription(ctx, task.ConversationKey, id, text); err != nil {
			o.log.Warn("cache chat image description failed", "task_id", task.ID, "image", id, "error", err)
		}
		o.log.Info("chat image described", "task_id", task.ID, "image", id, "role", role,
			"latency_ms", latency.Milliseconds())
	}
	if len(late) > 0 {
		reason := "out of time"
		if ctx.Err() != nil {
			reason = "cancelled"
		}
		notDescribed(slog.LevelWarn, late, role, reason, 0, dctx.Err())
	}
	return out
}

// markSeen records that the reply profile took in's seen chat images itself:
// later turns show them as seen earlier while they have no description (see
// contextbuilder.Input.SeenEarlier). A failure is logged.
func (o *Orchestrator) markSeen(ctx context.Context, task store.TaskRecord, in contextbuilder.Input) {
	var ids []string
	for _, c := range in.CurrentImages {
		if c.DataURL != "" {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := o.st.MarkChatImagesSeen(ctx, task.ConversationKey, ids); err != nil {
		o.log.Warn("mark chat images seen failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
	}
}

// describeSeenLater has the vision profile describe, in the background, the
// seen chat images of in that have no description yet, which the reply
// profile took itself, so later turns and text-only paths can show them as
// [图片：<描述>] (see describeChatImages, at admission.PriorityBackground). It
// returns at once: the description runs on bgCtx, which Close cancels and
// waits for, and none starts once the orchestrator is closing.
func (o *Orchestrator) describeSeenLater(task store.TaskRecord, in contextbuilder.Input) {
	var ids []string
	for _, c := range in.CurrentImages {
		if c.DataURL != "" && chatDescription(c.Description) == "" {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closing.Load() {
		return
	}
	o.bgWG.Add(1)
	go func() {
		defer o.bgWG.Done()
		o.describeChatImages(o.bgCtx, task, ids, admission.PriorityBackground)
	}()
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
