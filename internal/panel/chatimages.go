package panel

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// uploadChatImage stores the request body, the raw bytes of a PNG or JPEG
// image (its type is sniffed, the Content-Type header is ignored), as an
// image of the panel chat {key} not sent yet: a view copy and a reply copy
// (see imaging.ProcessChat), under a new ID; the original is not kept. Only a
// private panel chat takes images: another conversation is 400, an unknown
// one 404. A body over imaging.MaxBytes is 413; one that is not a PNG or
// JPEG, does not decode or is too large in pixels is 400; while another
// image upload is processed it is a retryable 409 CHAT_BUSY. It answers 201
// {id, width, height, reply_tokens} with the view copy's size and the reply
// copy's token estimate. The audit records the image's ID, size and bytes,
// never its pixels.
func (s *Server) uploadChatImage(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	if conv.Platform != store.PlatformPanel || conv.Type != conloomapi.ConvPrivate {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "只能在面板私聊里发图片")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, imaging.MaxBytes))
	if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
		s.writeError(w, r, http.StatusRequestEntityTooLarge, conloomapi.CodeInvalidEvent,
			fmt.Sprintf("图片不能超过 %d MiB", imaging.MaxBytes>>20))
		return
	}
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "could not read the request body", err)
		return
	}
	release, ok := s.tryImageSlot(w, r, s.imageUploads)
	if !ok {
		return
	}
	defer release()
	c, err := imaging.ProcessChat(data)
	if s.imageRejected(w, r, err) {
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	img := store.ChatImage{
		ID: ids.New("img"), ConversationKey: conv.Key,
		ViewJPEG: c.ViewJPEG, ViewWidth: c.ViewWidth, ViewHeight: c.ViewHeight,
		ReplyJPEG: c.ReplyJPEG, ReplyWidth: c.ReplyWidth, ReplyHeight: c.ReplyHeight, ReplyTokens: c.ReplyTokens,
		CreatedBy: panelActor(sess), CreatedAt: s.now().UTC(),
	}
	if err := s.d.Store.PutChatImage(r.Context(), img); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, sess, conv.BotInstanceID, "panel.chat.image.upload", conv.Key, struct {
		ImageID     string `json:"image_id"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		Bytes       int    `json:"bytes"`
		ReplyTokens int    `json:"reply_tokens"`
	}{img.ID, img.ViewWidth, img.ViewHeight, len(data), img.ReplyTokens})
	s.writeJSON(w, http.StatusCreated, struct {
		ID          string `json:"id"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		ReplyTokens int    `json:"reply_tokens"`
	}{img.ID, img.ViewWidth, img.ViewHeight, img.ReplyTokens})
}

// getChatImage serves the image {id} of the conversation {key} as a JPEG: the
// reply copy by default or with size=reply, the view copy with size=view;
// another size is 400. An image that is not the conversation's is 404, like
// an unknown one.
func (s *Server) getChatImage(w http.ResponseWriter, r *http.Request, _ *Session) {
	var data store.ChatImageData
	switch r.URL.Query().Get("size") {
	case "", "reply":
		data = store.ChatImageReply
	case "view":
		data = store.ChatImageView
	default:
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "size must be reply or view")
		return
	}
	id := r.PathValue("id")
	imgs, err := s.d.Store.GetChatImages(r.Context(), r.PathValue("key"), []string{id}, data)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	img, ok := imgs[id]
	if !ok {
		s.fail(w, r, fmt.Errorf("%w: chat image %s", store.ErrNotFound, id))
		return
	}
	body := img.ReplyJPEG
	if data == store.ChatImageView {
		body = img.ViewJPEG
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
