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

	createChat := func() string {
		t.Helper()
		status, raw := p.do(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": bot.ID, "display_name": "小明"})
		var created struct {
			Key string `json:"key"`
		}
		if status != http.StatusCreated || json.Unmarshal(raw, &created) != nil {
			t.Fatalf("create chat: status %d, body %s", status, raw)
		}
		return created.Key
	}
	// reply sends text in the chat key, whose bot messages so far end at
	// after, and returns the reply's seq and the model request answering it.
	reply := func(key string, after int64, text string) (int64, string) {
		t.Helper()
		p.send(key, text)
		got := p.botReplies(key, after, 1)
		if got[0].Text != fakeReply {
			t.Fatalf("reply = %+v, want %q", got, fakeReply)
		}
		reqs := fake.Requests()
		last, err := json.Marshal(reqs[len(reqs)-1])
		if err != nil {
			t.Fatal(err)
		}
		return got[0].Seq, string(last)
	}
	key := createChat()
	seq, last := reply(key, 0, "你好")
	for _, want := range []string{"data:image/jpeg;base64,", "外貌：", description} {
		if !strings.Contains(last, want) {
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

	// Deleting the template the bot uses: the bot falls back to default,
	// its chat keeps the persona it copied, and a new chat starts from
	// default.
	status, raw = p.do(http.MethodDelete, path, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"affected_bots":[{"id":"`+bot.ID+`","name":"小红","source":"panel"}]`) {
		t.Fatalf("delete template in use: status %d, body %s; want 200 naming 小红", status, raw)
	}
	var bots struct {
		Bots []struct {
			ID              string   `json:"id"`
			PersonaTemplate string   `json:"persona_template"`
			Problems        []string `json:"problems"`
		} `json:"bots"`
	}
	p.get("/panel/api/bots", &bots)
	for _, b := range bots.Bots {
		if b.ID == bot.ID && (b.PersonaTemplate != "default" || len(b.Problems) != 1 || !strings.Contains(b.Problems[0], "night-owl")) {
			t.Errorf("bot after its template was deleted = %+v, want default with the problem", b)
		}
	}
	if _, last := reply(key, seq, "还在吗"); !strings.Contains(last, description) {
		t.Errorf("reply request of the chat that copied the deleted template lacks its appearance")
	}
	if _, last := reply(createChat(), 0, "你好"); strings.Contains(last, description) || strings.Contains(last, "data:image/jpeg;base64,") {
		t.Errorf("reply request of a new chat after the delete still has the deleted template's appearance")
	}
}
