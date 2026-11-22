package panel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/gif"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// chatImageUpload POSTs data as an image of the conversation key.
func (h *writeHarness) chatImageUpload(key string, data []byte) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.write(http.MethodPost, convPath(key, "images"), string(data))
}

// unsentChatImages purges every unsent chat image and returns how many there
// were.
func (h *writeHarness) unsentChatImages() int {
	h.t.Helper()
	n, err := h.db.PurgeChatImages(context.Background(), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func jpegSize(t *testing.T, w *httptest.ResponseRecorder) (int, int) {
	t.Helper()
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("GET image: %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode served JPEG: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestChatImagesAPI_UploadAndGet(t *testing.T) {
	h := newWriteHarness(t, nil)
	ctx := context.Background()
	conv := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	data := pngData(t, testImage(2000, 1000, false))

	up := decodeUpload(t, h.chatImageUpload(conv.Key, data))
	if !strings.HasPrefix(up.ID, "img_") || up.Width != 1536 || up.Height != 768 || up.ReplyTokens != imaging.Tokens(768, 384) {
		t.Fatalf("upload = %+v", up)
	}
	got, err := h.db.GetChatImages(ctx, conv.Key, []string{up.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	img, ok := got[up.ID]
	if !ok {
		t.Fatalf("image %s not stored", up.ID)
	}
	if img.ConversationKey != conv.Key || img.MessageID != "" || img.CreatedBy != "panel:"+testUser ||
		img.ViewWidth != 1536 || img.ViewHeight != 768 || img.ReplyWidth != 768 || img.ReplyHeight != 384 {
		t.Errorf("stored image = %+v", img)
	}

	audits := h.audits("panel.chat.image.upload")
	if len(audits) != 1 {
		t.Fatalf("upload audits = %d, want 1", len(audits))
	}
	a := audits[0]
	if a.BotInstanceID != dataBot || a.Target != conv.Key || a.Actor != "panel:"+testUser {
		t.Errorf("audit = %+v", a)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(a.Detail), &detail); err != nil {
		t.Fatalf("audit detail %s: %v", a.Detail, err)
	}
	keys := make([]string, 0, len(detail))
	for k := range detail {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"bytes", "height", "image_id", "reply_tokens", "width"}; !slices.Equal(keys, want) {
		t.Errorf("audit detail keys = %v, want %v", keys, want)
	}
	if detail["image_id"] != up.ID || detail["width"] != 1536.0 || detail["height"] != 768.0 ||
		detail["bytes"] != float64(len(data)) || detail["reply_tokens"] != float64(up.ReplyTokens) {
		t.Errorf("audit detail = %s", a.Detail)
	}
	if b64 := base64.StdEncoding.EncodeToString(data); strings.Contains(a.Detail, b64[:40]) || strings.Contains(a.Detail, "base64") {
		t.Errorf("audit detail %s carries image data", a.Detail)
	}

	if w, hh := jpegSize(t, h.get(convPath(conv.Key, "images", up.ID))); w != 768 || hh != 384 {
		t.Errorf("GET reply copy = %d×%d, want 768×384", w, hh)
	}
	if w, hh := jpegSize(t, h.get(convPath(conv.Key, "images", up.ID)+"?size=reply")); w != 768 || hh != 384 {
		t.Errorf("GET size=reply = %d×%d, want 768×384", w, hh)
	}
	if w, hh := jpegSize(t, h.get(convPath(conv.Key, "images", up.ID)+"?size=view")); w != 1536 || hh != 768 {
		t.Errorf("GET size=view = %d×%d, want 1536×768", w, hh)
	}
	h.getErr(convPath(conv.Key, "images", up.ID)+"?size=original", http.StatusBadRequest, conloomapi.CodeInvalidEvent)

	// Every upload gets an ID of its own, even of the same bytes.
	again := decodeUpload(t, h.chatImageUpload(conv.Key, data))
	if again.ID == up.ID || !strings.HasPrefix(again.ID, "img_") {
		t.Errorf("second upload ID = %s, first %s", again.ID, up.ID)
	}
}

func TestChatImagesAPI_Rejects(t *testing.T) {
	h := newWriteHarness(t, nil)
	ctx := context.Background()
	conv := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	other := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	qq := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	small := pngData(t, testImage(20, 20, false))

	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, testImage(10, 10, false), nil); err != nil {
		t.Fatal(err)
	}
	e := errorBody(t, h.chatImageUpload(conv.Key, gifBuf.Bytes()), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if !strings.Contains(e.Message, "只支持 PNG 和 JPEG 图片") {
		t.Errorf("GIF message = %q", e.Message)
	}
	errorBody(t, h.chatImageUpload(conv.Key, make([]byte, imaging.MaxBytes+1)), http.StatusRequestEntityTooLarge, conloomapi.CodeInvalidEvent)
	e = errorBody(t, h.chatImageUpload(conv.Key, pngData(t, testImage(9000, 10, false))), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if !strings.Contains(e.Message, "9000×10") {
		t.Errorf("9000×10 message = %q", e.Message)
	}

	e = errorBody(t, h.chatImageUpload(qq.Key, small), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if e.Message != "只能在面板私聊里发图片" {
		t.Errorf("QQ chat message = %q", e.Message)
	}
	errorBody(t, h.chatImageUpload(writeGroup.Key, small), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.chatImageUpload(store.ConversationKey(dataBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_none"), small),
		http.StatusNotFound, conloomapi.CodeNotFound)

	h.srv.imageUploads <- struct{}{}
	e = errorBody(t, h.chatImageUpload(conv.Key, small), http.StatusConflict, conloomapi.CodeChatBusy)
	if e.Message != imageBusyMessage || !e.Retryable {
		t.Errorf("busy upload = %+v", e)
	}
	<-h.srv.imageUploads

	if n := len(h.audits("panel.chat.image.upload")); n != 0 {
		t.Errorf("upload audits after refusals = %d, want 0", n)
	}
	if n := h.unsentChatImages(); n != 0 {
		t.Errorf("chat images stored by refused uploads = %d, want 0", n)
	}

	// An image is served only under its own conversation.
	if err := h.db.PutChatImage(ctx, store.ChatImage{ID: "img_other", ConversationKey: other.Key,
		ViewJPEG: jpegData(t, testImage(20, 20, false)), ViewWidth: 20, ViewHeight: 20,
		ReplyJPEG: jpegData(t, testImage(20, 20, false)), ReplyWidth: 20, ReplyHeight: 20, CreatedAt: h.now}); err != nil {
		t.Fatal(err)
	}
	if w, hh := jpegSize(t, h.get(convPath(other.Key, "images", "img_other"))); w != 20 || hh != 20 {
		t.Errorf("own conversation GET = %d×%d", w, hh)
	}
	for _, q := range []string{"", "?size=view"} {
		h.getErr(convPath(conv.Key, "images", "img_other")+q, http.StatusNotFound, conloomapi.CodeNotFound)
	}
	h.getErr(convPath(conv.Key, "images", "img_unknown"), http.StatusNotFound, conloomapi.CodeNotFound)
}

func TestChatImagesAPI_AuthCSRFAndCSP(t *testing.T) {
	h := newWriteHarness(t, nil)
	conv := h.seedConv(store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	up := decodeUpload(t, h.chatImageUpload(conv.Key, pngData(t, testImage(40, 30, false))))
	path := convPath(conv.Key, "images", up.ID)

	w := h.do(http.MethodGet, path, "")
	errorBody(t, w, http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	if bytes.HasPrefix(w.Body.Bytes(), []byte{0xff, 0xd8}) {
		t.Error("unauthenticated GET served the JPEG")
	}

	data := string(pngData(t, testImage(50, 50, false)))
	errorBody(t, h.do(http.MethodPost, convPath(conv.Key, "images"), data, withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.do(http.MethodPost, convPath(conv.Key, "images"), data, withCSRF(h.csrf)), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	if n := len(h.audits("panel.chat.image.upload")); n != 1 {
		t.Errorf("upload audits = %d, want 1", n)
	}
	if n := h.unsentChatImages(); n != 1 {
		t.Errorf("chat images = %d, want only the accepted one", n)
	}

	up = decodeUpload(t, h.chatImageUpload(conv.Key, pngData(t, testImage(40, 30, false))))
	w = h.get(convPath(conv.Key, "images", up.ID))
	jpegSize(t, w)
	hd := w.Header()
	if hd.Get("Content-Security-Policy") != wantCSP || hd.Get("Cache-Control") != "no-store" || hd.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("GET headers = %v", hd)
	}
}
