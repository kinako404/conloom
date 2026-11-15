package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/store"
)

// putSizedImage stores a generated w×h PNG as a persona image.
func (e *testEnv) putSizedImage(t *testing.T, w, h int) store.PersonaImage {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(src.Pix); i += 4 {
		px := i / 4
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = uint8(px%w), uint8(px/w), 90, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	p, err := imaging.Process(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img := store.PersonaImage{ID: p.ID, MIME: p.MIME, Width: p.Width, Height: p.Height, Original: p.Original,
		ReplyJPEG: p.ReplyJPEG, ReplyWidth: p.ReplyWidth, ReplyHeight: p.ReplyHeight, ReplyTokens: p.ReplyTokens, CreatedBy: "test"}
	if err := e.st.PutPersonaImage(context.Background(), img); err != nil {
		t.Fatal(err)
	}
	return img
}

func TestDescribe_SendsImageAndRecordsUsage(t *testing.T) {
	const description = "她有一头齐肩的黑色短发，眼睛是浅棕色的。"
	env := newTestOrchestrator(t, testOpts{vision: answer("<think>先看发型，再看衣服。</think>\n\n  " + description + "  \n")})
	written := recordUsage(t, env)
	img := env.putSizedImage(t, 1600, 100)

	got, err := env.o.DescribeAppearance(context.Background(), img.ID, "admin")
	if err != nil {
		t.Fatalf("DescribeAppearance: %v", err)
	}
	if got != description {
		t.Errorf("text = %q, want %q: only the trimmed final answer", got, description)
	}

	reqs := env.vision.Requests()
	if len(reqs) != 1 || env.short.Calls() != 0 || env.long.Calls() != 0 {
		t.Fatalf("vision got %d requests, short %d, long %d; want one vision request", len(reqs), env.short.Calls(), env.long.Calls())
	}
	req := reqs[0]
	if req["model"] != "vision-model" || req["max_tokens"] != float64(256) {
		t.Errorf("model = %v, max_tokens = %v; want vision-model, the reserved output 256", req["model"], req["max_tokens"])
	}
	msgs := requestMessages(t, req)
	parts, _ := msgs[0]["content"].([]any)
	if len(msgs) != 1 || msgs[0]["role"] != "user" || len(parts) != 2 {
		t.Fatalf("messages = %.300v, want one user message with an image and a text part", msgs)
	}
	imagePart, _ := parts[0].(map[string]any)
	u, _ := imagePart["image_url"].(map[string]any)
	url, _ := u["url"].(string)
	b64, ok := strings.CutPrefix(url, "data:image/jpeg;base64,")
	if imagePart["type"] != "image_url" || !ok {
		t.Fatalf("first part = %.120v, want an image_url part with a JPEG data URL", imagePart)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode data URL: %v", err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != 1536 || cfg.Height != 96 {
		t.Errorf("sent image = %dx%d (%v), want the original scaled to 1536x96", cfg.Width, cfg.Height, err)
	}
	textPart, _ := parts[1].(map[string]any)
	if textPart["type"] != "text" || textPart["text"] != describePrompt {
		t.Errorf("second part = %v, want the fixed prompt", textPart)
	}
	for _, want := range []string{"150", "400", "第三人称", "发型", "服装", "背景", "画师", "文字", "标题", "列表"} {
		if !strings.Contains(describePrompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, describePrompt)
		}
	}

	calls := written()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.Purpose != store.PurposeVision || c.Role != "vision" || c.Model != "vision-model" || c.Outcome != store.OutcomeOK ||
		c.FinishReason != "stop" || c.BotInstanceID != "" || c.ConversationKey != "" || c.TaskID != "" {
		t.Errorf("call = %+v", c)
	}
	if c.CompletionTokens <= 0 || c.LatencyMS < 0 {
		t.Errorf("completion tokens %d, latency %dms", c.CompletionTokens, c.LatencyMS)
	}
	if s := env.o.adm.Stats("vision"); s.InFlight != 0 || s.Waiting != 0 {
		t.Errorf("admission stats after the call = %+v, want the lease released", s)
	}
	logs := env.logs.String()
	if !strings.Contains(logs, `msg="appearance described"`) || !strings.Contains(logs, "actor=admin") || !strings.Contains(logs, img.ID) {
		t.Errorf("logs lack the description record:\n%s", logs)
	}
	if strings.Contains(logs, "base64") || strings.Contains(logs, b64[:32]) {
		t.Errorf("logs carry the image bytes:\n%.500s", logs)
	}
}

func TestDescribe_SeparateReasoningLeftOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"vision-model","choices":[{"index":0,"finish_reason":"stop",`+
			`"message":{"role":"assistant","reasoning_content":"先看发型。","content":"他穿着一件灰色外套。"}}],`+
			`"usage":{"prompt_tokens":300,"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":30}}}`)
	}))
	t.Cleanup(srv.Close)
	env := newTestOrchestrator(t, testOpts{vision: answer("不会用到"), configure: func(c *config.Config) {
		p := c.ModelProfiles["vision"]
		p.Endpoint = srv.URL + "/v1"
		c.ModelProfiles["vision"] = p
	}})
	written := recordUsage(t, env)
	img := env.putTestImage(t)
	got, err := env.o.DescribeAppearance(context.Background(), img.ID, "admin")
	if err != nil || got != "他穿着一件灰色外套。" {
		t.Errorf("DescribeAppearance = %q, %v; want only the final answer", got, err)
	}
	if calls := written(); len(calls) != 1 || calls[0].PromptTokens != 300 || calls[0].CompletionTokens != 40 || calls[0].ReasoningTokens != 30 {
		t.Errorf("calls = %+v, want the reported tokens", calls)
	}
}

func TestDescribe_NoVision503(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	img := env.putTestImage(t)
	if _, err := env.o.DescribeAppearance(context.Background(), img.ID, "admin"); !errors.Is(err, ErrNoVision) {
		t.Fatalf("err = %v, want ErrNoVision", err)
	}
	if env.short.Calls() != 0 || env.long.Calls() != 0 {
		t.Errorf("short %d, long %d calls; want no model called", env.short.Calls(), env.long.Calls())
	}

	env = newTestOrchestrator(t, testOpts{vision: answer("不会用到")})
	if _, err := env.o.DescribeAppearance(context.Background(), strings.Repeat("ab", 32), "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown image: err = %v, want store.ErrNotFound", err)
	}
	if env.vision.Calls() != 0 {
		t.Errorf("unknown image: vision called %d times, want 0", env.vision.Calls())
	}
}

func TestDescribe_Timeout(t *testing.T) {
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{vision: func(map[string]any) (int, string) {
		<-release
		return 200, "太晚了"
	}})
	t.Cleanup(func() { close(release) })
	if env.o.describeTimeout != 40*time.Second {
		t.Errorf("describe timeout = %v, want 40s", env.o.describeTimeout)
	}
	written := recordUsage(t, env)
	img := env.putTestImage(t)
	env.o.describeTimeout = 100 * time.Millisecond

	start := time.Now()
	_, err := env.o.DescribeAppearance(context.Background(), img.ID, "admin")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v, want the describe timeout to stop it", d)
	}
	if calls := written(); len(calls) != 1 || calls[0].Outcome != store.OutcomeTimeout || calls[0].Purpose != store.PurposeVision {
		t.Errorf("calls = %+v, want one timed-out vision call", calls)
	}
	if s := env.o.adm.Stats("vision"); s.InFlight != 0 {
		t.Errorf("admission stats after the timeout = %+v, want the lease released", s)
	}
}

func TestMaxTokenFactor_ExcludesVision(t *testing.T) {
	profiles := map[string]config.ModelProfile{
		"short_context": {TokenEstimateFactor: 1.2},
		"long_context":  {TokenEstimateFactor: 1.5},
		"vision":        {TokenEstimateFactor: 3},
	}
	if f := maxTokenFactor(profiles); f != 1.5 {
		t.Errorf("maxTokenFactor = %v, want 1.5: the vision profile does not count", f)
	}
	delete(profiles, "short_context")
	delete(profiles, "long_context")
	if f := maxTokenFactor(profiles); f != 1 {
		t.Errorf("maxTokenFactor with only vision = %v, want 1", f)
	}
}
