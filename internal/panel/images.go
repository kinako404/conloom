package panel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// AppearanceService describes the appearance shown by a persona image with
// the vision model; *core.Orchestrator is one.
type AppearanceService interface {
	DescribeAppearance(ctx context.Context, imageID, actor string) (string, error)
}

const imageBusyMessage = "正在处理另一张图片，请稍后"

// tryImageSlot takes slot, a channel with room for one request, writing a
// retryable 409 CHAT_BUSY instead when another request holds it; release
// frees it. Requests are refused rather than queued: a description can take
// 40 seconds, and the server's write timeout is 45.
func (s *Server) tryImageSlot(w http.ResponseWriter, r *http.Request, slot chan struct{}) (release func(), ok bool) {
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, true
	default:
		s.writeError(w, r, http.StatusConflict, conloomapi.CodeChatBusy, imageBusyMessage)
		return nil, false
	}
}

// uploadImage stores the request body, the raw bytes of a PNG or JPEG image
// (its type is sniffed, the Content-Type header is ignored), as a persona
// image with its reply copy, and answers 201 with its ID, size and the reply
// copy's token estimate. A body over imaging.MaxBytes is 413; one that is not
// a PNG or JPEG, does not decode or is too large in pixels is 400. Uploading
// the same bytes again gives the same ID and restarts the image's purge grace.
func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request, sess *Session) {
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
	p, err := imaging.Process(data)
	switch {
	case errors.Is(err, imaging.ErrUnsupported):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			fmt.Sprintf("只支持 PNG 和 JPEG 图片（%s）", strings.TrimPrefix(err.Error(), imaging.ErrUnsupported.Error()+": ")), err)
		return
	case errors.Is(err, imaging.ErrTooLarge):
		s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			fmt.Sprintf("图片太大：宽和高都不能超过 %d 像素，总共不超过 %d 万像素（%s）", imaging.MaxEdge, imaging.MaxPixels/10000,
				strings.TrimPrefix(err.Error(), imaging.ErrTooLarge.Error()+": ")), err)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	err = s.d.Store.PutPersonaImage(r.Context(), store.PersonaImage{
		ID: p.ID, MIME: p.MIME, Width: p.Width, Height: p.Height, Original: p.Original, ReplyJPEG: p.ReplyJPEG,
		ReplyWidth: p.ReplyWidth, ReplyHeight: p.ReplyHeight, ReplyTokens: p.ReplyTokens,
		CreatedBy: panelActor(sess), CreatedAt: s.now().UTC(),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, sess, "", "persona.image.upload", p.ID, struct {
		MIME        string `json:"mime"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		Bytes       int    `json:"bytes"`
		ReplyTokens int    `json:"reply_tokens"`
	}{p.MIME, p.Width, p.Height, len(p.Original), p.ReplyTokens})
	s.writeJSON(w, http.StatusCreated, struct {
		ID          string `json:"id"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		ReplyTokens int    `json:"reply_tokens"`
	}{p.ID, p.Width, p.Height, p.ReplyTokens})
}

// getImage serves the persona image {id}: the reply copy (a JPEG) by default
// or with size=reply, the original as uploaded with size=original.
func (s *Server) getImage(w http.ResponseWriter, r *http.Request, _ *Session) {
	var original bool
	switch r.URL.Query().Get("size") {
	case "", "reply":
	case "original":
		original = true
	default:
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "size must be reply or original")
		return
	}
	img, err := s.d.Store.GetPersonaImage(r.Context(), r.PathValue("id"), original)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	mime, data := "image/jpeg", img.ReplyJPEG
	if original {
		mime, data = img.MIME, img.Original
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// describeImage asks the vision model to describe the appearance shown by the
// persona image {id} and answers {text}, which is not stored. An unknown
// image is 404; no vision model is 503 MODEL_UNAVAILABLE; a description not
// done in time is 504 and another failure of the model 502, both
// MODEL_UNAVAILABLE. An upstream error status is logged without the response
// body, which may echo the request's image.
func (s *Server) describeImage(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.d.Appearance == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "appearance descriptions are not available")
		return
	}
	release, ok := s.tryImageSlot(w, r, s.imageDescribes)
	if !ok {
		return
	}
	defer release()
	id := r.PathValue("id")
	text, err := s.d.Appearance.DescribeAppearance(r.Context(), id, panelActor(sess))
	switch {
	case err == nil:
	case r.Context().Err() != nil, errors.Is(err, store.ErrNotFound), errors.Is(err, core.ErrNoVision):
		s.fail(w, r, err)
		return
	case errors.Is(err, context.DeadlineExceeded):
		s.respondError(w, r, http.StatusGatewayTimeout, conloomapi.CodeModelUnavailable, "识图超时，请稍后重试", err)
		return
	default:
		cause := err
		if se := (*openai.StatusError)(nil); errors.As(err, &se) {
			cause = fmt.Errorf("vision model answered status %d", se.Code)
		}
		s.respondError(w, r, http.StatusBadGateway, conloomapi.CodeModelUnavailable, "识图模型调用失败", cause)
		return
	}
	s.audit(r, sess, "", "persona.appearance.describe", id, struct {
		Chars int `json:"chars"`
	}{utf8.RuneCountInString(text)})
	s.writeJSON(w, http.StatusOK, struct {
		Text string `json:"text"`
	}{text})
}
