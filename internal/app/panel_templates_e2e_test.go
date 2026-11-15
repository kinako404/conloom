package app_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/panel"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// uploadImage POSTs data as the raw body of a persona image upload.
func (c *panelClient) uploadImage(data []byte) (int, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodPost, c.base+"/panel/api/persona-images", bytes.NewReader(data))
	if err != nil {
		c.t.Fatal(err)
	}
	req.AddCookie(c.cookie)
	req.Header.Set("X-CSRF-Token", c.csrf)
	req.Header.Set("Content-Type", "image/png")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func TestPanelE2E_TemplateWithAppearanceImage(t *testing.T) {
	const description = "她留着及肩的黑色短发，穿一件灰色的旧毛衣。"
	fake := fakeopenai.New(t, answer(fakeReply))
	vision := fakeopenai.New(t, answer(description))
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	cfg := panelTestConfig(t, configParams{modelURL: fake.URL}, hash)
	short := cfg.ModelProfiles["short_context"]
	short.Modalities = []string{"text", "image"}
	cfg.ModelProfiles["short_context"] = short
	v := short
	v.Endpoint, v.Model, v.Modalities = vision.URL, "vision-model", []string{"text", "image"}
	cfg.ModelProfiles["vision"] = v
	e := newEnv(t, cfg)
	p := panelLogin(t, e.srv.URL)

	img := image.NewRGBA(image.Rect(0, 0, 900, 300))
	for y := range 300 {
		for x := range 900 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	status, raw := p.uploadImage(buf.Bytes())
	var up struct {
		ID string `json:"id"`
	}
	if status != http.StatusCreated || json.Unmarshal(raw, &up) != nil || len(up.ID) != 64 {
		t.Fatalf("upload: status %d, body %s", status, raw)
	}

	status, raw = p.do(http.MethodPost, "/panel/api/persona-images/"+up.ID+"/describe", nil)
	var described struct {
		Text string `json:"text"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &described) != nil || described.Text != description {
		t.Fatalf("describe: status %d, body %s", status, raw)
	}
	if n := len(vision.Requests()); n != 1 {
		t.Fatalf("vision requests = %d, want 1", n)
	}

	doc := conloomapi.DefaultPersona()
	doc.PersonaName, doc.CharacterName = "夜猫", "小夜"
	doc.Appearance, doc.AppearanceImage = described.Text, up.ID
	path := "/panel/api/persona-templates/night-owl"
	if status, raw := p.do(http.MethodPut, path, map[string]any{"persona": doc, "reason": "外观"}); status != http.StatusCreated {
		t.Fatalf("PUT template: status %d, body %s", status, raw)
	}
	status, raw = p.do(http.MethodPost, "/panel/api/bots", map[string]any{"name": "小红", "persona_template": "night-owl", "skills": []string{}})
	var bot struct {
		ID string `json:"id"`
	}
	if status != http.StatusCreated || json.Unmarshal(raw, &bot) != nil {
		t.Fatalf("create bot: status %d, body %s", status, raw)
	}
	if status, raw := p.do(http.MethodDelete, path, nil); status != http.StatusConflict || !strings.Contains(string(raw), "小红") {
		t.Fatalf("delete template in use: status %d, body %s; want 409 naming 小红", status, raw)
	}

	status, raw = p.do(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": bot.ID, "display_name": "小明"})
	var created struct {
		Key string `json:"key"`
	}
	if status != http.StatusCreated || json.Unmarshal(raw, &created) != nil {
		t.Fatalf("create chat: status %d, body %s", status, raw)
	}
	p.send(created.Key, "你好")
	if got := p.botReplies(created.Key, 0, 1); got[0].Text != fakeReply {
		t.Fatalf("reply = %+v, want %q", got, fakeReply)
	}
	reqs := fake.Requests()
	last, err := json.Marshal(reqs[len(reqs)-1])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"data:image/jpeg;base64,", "外貌：", description} {
		if !strings.Contains(string(last), want) {
			t.Errorf("reply request lacks %q", want)
		}
	}

	req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/panel/api/persona-images/"+url.PathEscape(up.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(p.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("GET image: status %d, type %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
