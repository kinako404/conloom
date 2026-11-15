package panel

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const imagesPath = "/panel/api/persona-images"

func imagePath(id string, rest ...string) string {
	p := imagesPath + "/" + id
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

// testImage is a w×h gradient, half transparent when alpha.
func testImage(w, h int, alpha bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			a := uint8(255)
			if alpha && x < w/2 {
				a = 0
			}
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: a})
		}
	}
	return img
}

func pngData(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegData(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bombPNG is a PNG whose header claims a w×h grayscale image and whose pixel
// data stops after the first row.
func bombPNG(t *testing.T, w, h uint32) []byte {
	t.Helper()
	chunk := func(buf *bytes.Buffer, typ string, data []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(data)))
		buf.Write(n[:])
		buf.WriteString(typ)
		buf.Write(data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		binary.BigEndian.PutUint32(n[:], crc.Sum32())
		buf.Write(n[:])
	}
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8
	chunk(&buf, "IHDR", ihdr)
	var idat bytes.Buffer
	zw := zlib.NewWriter(&idat)
	if _, err := zw.Write(make([]byte, 1+int(w))); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	chunk(&buf, "IDAT", idat.Bytes())
	chunk(&buf, "IEND", nil)
	return buf.Bytes()
}

func imageID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func withContentType(v string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Content-Type", v) }
}

// upload POSTs data as an image labelled contentType.
func (h *writeHarness) upload(data []byte, contentType string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, imagesPath, string(data), withCookie(h.cookie), withCSRF(h.csrf), withContentType(contentType))
}

type uploadResp struct {
	ID          string `json:"id"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ReplyTokens int    `json:"reply_tokens"`
}

func decodeUpload(t *testing.T, w *httptest.ResponseRecorder) uploadResp {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201 (body %s)", w.Code, w.Body)
	}
	var v uploadResp
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}

func (h *writeHarness) imageStored(id string) bool {
	h.t.Helper()
	ok, err := h.db.PersonaImageExists(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return ok
}

func TestImagesAPI_UploadGetAndAudit(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()
	data := pngData(t, testImage(1000, 500, true))

	up := decodeUpload(t, h.upload(data, "image/png"))
	want := uploadResp{ID: imageID(data), Width: 1000, Height: 500, ReplyTokens: imaging.Tokens(768, 384)}
	if up != want {
		t.Fatalf("upload = %+v, want %+v", up, want)
	}
	img, err := h.db.GetPersonaImage(ctx, up.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if img.MIME != "image/png" || !bytes.Equal(img.Original, data) || img.CreatedBy != "panel:"+testUser ||
		!img.CreatedAt.Equal(h.now) || img.ReplyWidth != 768 || img.ReplyHeight != 384 {
		t.Errorf("stored image = mime %s, by %s at %v, reply %d×%d", img.MIME, img.CreatedBy, img.CreatedAt, img.ReplyWidth, img.ReplyHeight)
	}

	w := h.get(imagePath(up.ID) + "?size=original")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), data) {
		t.Errorf("GET original: %d %s, %d bytes", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("GET original headers = %v", w.Header())
	}
	for _, q := range []string{"", "?size=reply"} {
		w := h.get(imagePath(up.ID) + q)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("GET%s: %d %s", q, w.Code, w.Header().Get("Content-Type"))
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
		if err != nil || cfg.Width != 768 || cfg.Height != 384 || w.Header().Get("Content-Length") != fmt.Sprint(w.Body.Len()) {
			t.Errorf("GET%s reply copy = %+v, %v (length header %s)", q, cfg, err, w.Header().Get("Content-Length"))
		}
	}
	h.getErr(imagePath(up.ID)+"?size=huge", http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	h.getErr(imagePath(strings.Repeat("0", 64)), http.StatusNotFound, conloomapi.CodeNotFound)

	// The type is sniffed: a JPEG labelled as PNG is stored as a JPEG.
	jp := jpegData(t, testImage(300, 600, false))
	up2 := decodeUpload(t, h.upload(jp, "image/png"))
	if up2.ID != imageID(jp) || up2.Width != 300 || up2.Height != 600 || up2.ReplyTokens != imaging.Tokens(300, 600) {
		t.Errorf("JPEG upload = %+v", up2)
	}
	if w := h.get(imagePath(up2.ID) + "?size=original"); w.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("JPEG original served as %s", w.Header().Get("Content-Type"))
	}
	// The same bytes again give the same ID.
	if again := decodeUpload(t, h.upload(data, "application/octet-stream")); again != want {
		t.Errorf("re-upload = %+v, want %+v", again, want)
	}

	details := checkTemplateAudits(t, h, "persona.image.upload", up.ID, up2.ID, up.ID)
	b64 := base64.StdEncoding.EncodeToString(data)
	for _, d := range details {
		if len(d) > 300 || strings.Contains(d, b64[:40]) {
			t.Errorf("upload audit detail %s carries image data", d)
		}
	}
	if !someContains(details, `"mime":"image/png"`, `"width":1000`, `"height":500`, fmt.Sprintf(`"bytes":%d`, len(data))) {
		t.Errorf("upload audit details %v lack the PNG's mime, size and bytes", details)
	}

	// A template may now reference the image.
	doc := lighthousePersona()
	doc.AppearanceImage = up.ID
	if got := decodeTemplate(t, h.putTemplate("with-image", doc, ""), http.StatusCreated); got.AppearanceImage != up.ID {
		t.Errorf("template with image = %+v", got)
	}
}

func TestImagesAPI_Rejects(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()
	gifData := func() []byte {
		var buf bytes.Buffer
		if err := gif.Encode(&buf, testImage(10, 10, false), nil); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}()
	tooBig := append(pngData(t, testImage(4, 4, false)), make([]byte, imaging.MaxBytes)...)

	errorBody(t, h.upload(tooBig, "image/png"), http.StatusRequestEntityTooLarge, conloomapi.CodeInvalidEvent)
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, "PNG"},
		{"text labelled PNG", []byte("this is not an image at all"), "PNG"},
		{"GIF", gifData, "PNG"},
		{"truncated PNG", pngData(t, testImage(40, 40, false))[:60], "PNG"},
		{"20000×20000 header", bombPNG(t, 20000, 20000), "20000×20000"},
		{"8001 wide", bombPNG(t, 8001, 10), "8001×10"},
		{"over 25 MP", bombPNG(t, 6000, 5000), "6000×5000"},
	} {
		e := errorBody(t, h.upload(c.data, "image/png"), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if !strings.Contains(e.Message, c.want) {
			t.Errorf("%s: message %q lacks %q", c.name, e.Message, c.want)
		}
		if h.imageStored(imageID(c.data)) {
			t.Errorf("%s: image stored", c.name)
		}
	}
	if h.imageStored(imageID(tooBig)) {
		t.Error("too big image stored")
	}

	// One upload is processed at a time; another is refused meanwhile.
	data := pngData(t, testImage(20, 20, false))
	h.srv.imageUploads <- struct{}{}
	e := errorBody(t, h.upload(data, "image/png"), http.StatusConflict, conloomapi.CodeChatBusy)
	if e.Message != "正在处理另一张图片，请稍后" || !e.Retryable {
		t.Errorf("busy upload = %+v", e)
	}
	<-h.srv.imageUploads
	decodeUpload(t, h.upload(data, "image/png"))

	if n := len(h.audits("persona.image.upload")); n != 1 {
		t.Errorf("upload audits = %d, want 1", n)
	}
	if _, err := h.db.GetPersonaImage(ctx, imageID(data), false); err != nil {
		t.Errorf("accepted image not stored: %v", err)
	}
}

func TestImagesAPI_CSRF(t *testing.T) {
	h := newBotsHarness(t)
	data := pngData(t, testImage(20, 20, false))
	up := decodeUpload(t, h.upload(data, "image/png"))
	other := string(pngData(t, testImage(30, 30, false)))
	doc := mustJSON(t, map[string]any{"persona": lighthousePersona()})

	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, imagesPath, other},
		{http.MethodPost, imagePath(up.ID, "describe"), ""},
		{http.MethodPut, templatePath("night-owl"), doc},
		{http.MethodPost, templatePath("cat", "reset"), ""},
		{http.MethodDelete, templatePath("cat"), ""},
	} {
		errorBody(t, h.do(req.method, req.path, req.body, withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(req.method, req.path, req.body, withCookie(h.cookie), withCSRF("wrong")),
			http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(req.method, req.path, req.body, withCookie(h.cookie), withCSRF(h.csrf), withOrigin("http://attacker.invalid")),
			http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(req.method, req.path, req.body, withCSRF(h.csrf)), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	}
	for _, p := range []string{"/panel/api/persona-templates", templatePath("cat"), imagePath(up.ID)} {
		errorBody(t, h.do(http.MethodGet, p, ""), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	}

	if h.imageStored(imageID([]byte(other))) {
		t.Error("refused upload stored")
	}
	if _, ok := h.personas.Template("night-owl"); ok {
		t.Error("refused template PUT stored")
	}
	for kind, n := range map[string]int{"persona.image.upload": 1, "persona.appearance.describe": 0,
		"persona.template.put": 0, "persona.template.reset": 0, "persona.template.delete": 0} {
		if got := len(h.audits(kind)); got != n {
			t.Errorf("%s audits = %d, want %d", kind, got, n)
		}
	}
}

// fakeAppearance describes images as its describe func does, recording who
// asked.
type fakeAppearance struct {
	mu       sync.Mutex
	describe func(ctx context.Context, id string) (string, error)
	calls    []string
}

func (f *fakeAppearance) DescribeAppearance(ctx context.Context, imageID, actor string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, imageID+" by "+actor)
	describe := f.describe
	f.mu.Unlock()
	return describe(ctx, imageID)
}

func TestDescribeAPI_NoVision503(t *testing.T) {
	// Without the service the API answers 503.
	u := newWriteHarness(t, nil)
	errorBody(t, u.write(http.MethodPost, imagePath(strings.Repeat("0", 64), "describe"), nil),
		http.StatusServiceUnavailable, conloomapi.CodeInternal)

	// The orchestrator without a vision profile answers 503 未配置识图模型.
	h := newBotsHarness(t)
	up := decodeUpload(t, h.upload(pngData(t, testImage(20, 20, false)), "image/png"))
	e := errorBody(t, h.write(http.MethodPost, imagePath(up.ID, "describe"), nil), http.StatusServiceUnavailable, conloomapi.CodeModelUnavailable)
	if e.Message != "未配置识图模型" || e.Retryable {
		t.Errorf("no vision error = %+v, want 未配置识图模型, not retryable", e)
	}
	if n := len(h.audits("persona.appearance.describe")); n != 0 {
		t.Errorf("describe audits after failure = %d, want 0", n)
	}
}

// lockedBuffer is a bytes.Buffer safe for concurrent use.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestDescribeAPI_ResultsAndErrors(t *testing.T) {
	fake := &fakeAppearance{}
	logs := &lockedBuffer{}
	h := newWriteHarness(t, func(d *Deps) {
		d.Appearance, d.Logger = fake, slog.New(slog.NewTextHandler(logs, nil))
	})
	id := strings.Repeat("ab", 32)
	describe := func() *httptest.ResponseRecorder {
		return h.write(http.MethodPost, imagePath(id, "describe"), nil)
	}

	text := "她留着及肩的黑色短发，眼睛是浅褐色的。"
	fake.describe = func(context.Context, string) (string, error) { return text, nil }
	w := describe()
	var got struct {
		Text string `json:"text"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Text != text {
		t.Fatalf("describe = %d %s", w.Code, w.Body)
	}
	if want := id + " by panel:" + testUser; len(fake.calls) != 1 || fake.calls[0] != want {
		t.Errorf("calls = %v, want [%s]", fake.calls, want)
	}
	details := checkTemplateAudits(t, h, "persona.appearance.describe", id)
	if !someContains(details, fmt.Sprintf(`"chars":%d`, len([]rune(text)))) || strings.Contains(details[0], "黑色") {
		t.Errorf("describe audit details = %v, want the length and not the text", details)
	}

	for _, c := range []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
	}{
		{"unknown image", fmt.Errorf("lookup: %w", store.ErrNotFound), http.StatusNotFound, conloomapi.CodeNotFound, false},
		{"timeout", fmt.Errorf("model vision: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, conloomapi.CodeModelUnavailable, true},
		{"admission", errors.New("admission vision: request exceeds token budget"), http.StatusBadGateway, conloomapi.CodeModelUnavailable, false},
		{"upstream", fmt.Errorf("model vision: %w", &openai.StatusError{Code: 500, Body: `{"echo":"data:image/jpeg;base64,/9j/4AAQ"}`}),
			http.StatusBadGateway, conloomapi.CodeModelUnavailable, false},
		{"no vision model", fmt.Errorf("describe: %w", core.ErrNoVision), http.StatusServiceUnavailable, conloomapi.CodeModelUnavailable, false},
	} {
		fake.describe = func(context.Context, string) (string, error) { return "", c.err }
		e := errorBody(t, describe(), c.status, c.code)
		if strings.Contains(e.Message, "budget") || strings.Contains(e.Message, "status") {
			t.Errorf("%s: message %q leaks the cause", c.name, e.Message)
		}
		if e.Retryable != c.retryable {
			t.Errorf("%s: retryable = %v, want %v", c.name, e.Retryable, c.retryable)
		}
	}
	if l := logs.String(); strings.Contains(l, "base64") || !strings.Contains(l, "vision model answered status 500") ||
		!strings.Contains(l, "request exceeds token budget") {
		t.Errorf("logs = %s, want the causes without the upstream body", l)
	}

	// One description runs at a time; another is refused meanwhile.
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	fake.describe = func(context.Context, string) (string, error) {
		entered <- struct{}{}
		<-release
		return text, nil
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- describe() }()
	<-entered
	e := errorBody(t, describe(), http.StatusConflict, conloomapi.CodeChatBusy)
	if e.Message != "正在处理另一张图片，请稍后" {
		t.Errorf("busy describe message = %q", e.Message)
	}
	// Uploads have their own slot.
	decodeUpload(t, h.upload(pngData(t, testImage(20, 20, false)), "image/png"))
	unblock()
	if w := <-first; w.Code != http.StatusOK {
		t.Errorf("first describe = %d %s", w.Code, w.Body)
	}
	if n := len(h.audits("persona.appearance.describe")); n != 2 {
		t.Errorf("describe audits = %d, want 2 (the successful ones)", n)
	}
}
