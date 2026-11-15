package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// withImageModality lets the named model profiles take images.
func withImageModality(roles ...string) func(*config.Config) {
	return func(c *config.Config) {
		for _, role := range roles {
			p := c.ModelProfiles[role]
			p.Modalities = []string{"text", "image"}
			c.ModelProfiles[role] = p
		}
	}
}

// putTestImage stores a generated 300×200 PNG as a persona image.
func (e *testEnv) putTestImage(t *testing.T) store.PersonaImage {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for y := range 200 {
		for x := range 300 {
			src.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
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

// setAppearance stores a default persona for convKey with appearance image id.
func (e *testEnv) setAppearance(t *testing.T, convKey, id string, skip bool) {
	t.Helper()
	p := conloomapi.DefaultPersona()
	p.AppearanceImage, p.AppearanceImageSkip = id, skip
	if _, err := e.o.personas.Update(context.Background(), convKey, "default", conloomapi.PersonaPut{Persona: &p}, "test"); err != nil {
		t.Fatal(err)
	}
}

// replyTo submits text from 小明 (20001) in the test group, or in his private
// chat, and waits for fakeReply to be sent.
func (e *testEnv) replyTo(t *testing.T, private bool, text string) {
	t.Helper()
	ev := e.groupEvent("20001", text, withName("小明"), mentionBot)
	if private {
		ev = e.event(conloomapi.ConvPrivate, "20001", "20001", text, withName("小明"))
	}
	task := e.waitTask(t, e.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
}

func dataURL(img store.PersonaImage) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img.ReplyJPEG)
}

// requestImages returns the image URLs in req's messages and whether any of
// their text carries the appearance label.
func requestImages(t *testing.T, req map[string]any) (urls []string, labelled bool) {
	t.Helper()
	for _, m := range requestMessages(t, req) {
		switch c := m["content"].(type) {
		case string:
			labelled = labelled || strings.Contains(c, contextbuilder.AppearanceLabel)
		case []any:
			for _, p := range c {
				part, _ := p.(map[string]any)
				switch part["type"] {
				case "image_url":
					u, _ := part["image_url"].(map[string]any)
					url, _ := u["url"].(string)
					urls = append(urls, url)
				case "text":
					text, _ := part["text"].(string)
					labelled = labelled || strings.Contains(text, contextbuilder.AppearanceLabel)
				}
			}
		}
	}
	return urls, labelled
}

func assertNoImage(t *testing.T, what string, req map[string]any) {
	t.Helper()
	if urls, labelled := requestImages(t, req); len(urls) > 0 || labelled {
		t.Errorf("%s: request carries %d images (labelled %v), want none", what, len(urls), labelled)
	}
}

// assertImageSent checks that img and its label start the first user message
// of req, the message after the system prompt, and appear nowhere else.
func assertImageSent(t *testing.T, what string, req map[string]any, img store.PersonaImage) {
	t.Helper()
	msgs := requestMessages(t, req)
	parts, _ := msgs[1]["content"].([]any)
	if msgs[1]["role"] != "user" || len(parts) != 2 {
		t.Fatalf("%s: first user message = %v, want an image and a text part", what, msgs[1])
	}
	image, _ := parts[0].(map[string]any)
	u, _ := image["image_url"].(map[string]any)
	if image["type"] != "image_url" || u["url"] != dataURL(img) {
		t.Errorf("%s: first part = %.120v, want the reply copy as a data URL", what, image)
	}
	text, _ := parts[1].(map[string]any)
	if s, _ := text["text"].(string); text["type"] != "text" || !strings.HasPrefix(s, contextbuilder.AppearanceLabel+"\n") {
		t.Errorf("%s: second part = %v, want text starting with the label line", what, text)
	}
	if urls, _ := requestImages(t, req); len(urls) != 1 {
		t.Errorf("%s: request carries %d images, want 1", what, len(urls))
	}
}

func TestReplyImage_SentWhenProfileSupportsImage(t *testing.T) {
	for _, private := range []bool{false, true} {
		name := map[bool]string{false: "group", true: "private"}[private]
		t.Run(name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
				withImageModality("short_context")(c)
				c.Bots[0].PrivateChat = "all"
			}})
			img := env.putTestImage(t)
			key := testConvKey
			if private {
				key = privKeyOf("20001")
			}
			env.setAppearance(t, key, img.ID, false)
			env.replyTo(t, private, "在吗")
			req := env.short.Requests()[0]
			assertImageSent(t, name, req, img)
			msgs := requestMessages(t, req)
			parts, _ := msgs[len(msgs)-1]["content"].([]any)
			if text, _ := parts[len(parts)-1].(map[string]any)["text"].(string); len(msgs) != 2 || !strings.Contains(text, "【当前消息】发送者：小明\n") {
				t.Errorf("messages = %v, want the image on the current message", msgs)
			}
		})
	}
}

func TestReplyImage_DroppedWithoutImageModality(t *testing.T) {
	estimate := func(withImage bool) (map[string]any, int, store.PersonaImage) {
		env := newTestOrchestrator(t, testOpts{})
		written := recordUsage(t, env)
		img := env.putTestImage(t)
		if withImage {
			env.setAppearance(t, testConvKey, img.ID, false)
		}
		env.replyTo(t, false, "在吗")
		calls := written()
		if len(calls) != 1 {
			t.Fatalf("recorded %d calls, want 1", len(calls))
		}
		return env.short.Requests()[0], calls[0].EstimatedTokens, img
	}
	plainReq, plain, _ := estimate(false)
	req, got, img := estimate(true)
	assertNoImage(t, "text-only model", req)
	if a, b := requestText(t, req), requestText(t, plainReq); a != b {
		t.Errorf("request text differs from a persona without image:\n%s\nwant\n%s", a, b)
	}
	if got < plain+img.ReplyTokens {
		t.Errorf("estimated tokens = %d, want at least %d + %d: the estimate counts the image though it is dropped", got, plain, img.ReplyTokens)
	}
}

func TestReplyImage_DroppedOnReroute(t *testing.T) {
	fail := func(map[string]any) (int, string) { return 500, "boom" }
	t.Run("rerouted to a text-only model", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{short: fail, configure: withImageModality("short_context")})
		img := env.putTestImage(t)
		env.setAppearance(t, testConvKey, img.ID, false)
		env.replyTo(t, false, "在吗")
		assertImageSent(t, "short_context", env.short.Requests()[0], img)
		assertNoImage(t, "long_context", env.long.Requests()[0])
	})
	t.Run("rerouted to an image model", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{short: fail, configure: withImageModality("long_context")})
		img := env.putTestImage(t)
		env.setAppearance(t, testConvKey, img.ID, false)
		env.replyTo(t, false, "在吗")
		assertNoImage(t, "short_context", env.short.Requests()[0])
		assertImageSent(t, "long_context", env.long.Requests()[0], img)
	})
}

func TestReplyImage_ToolRoundsFollowProfile(t *testing.T) {
	for _, image := range []bool{false, true} {
		t.Run(map[bool]string{false: "text-only", true: "image"}[image], func(t *testing.T) {
			configure := func(*config.Config) {}
			if image {
				configure = withImageModality("short_context")
			}
			env := skillEnv(t, testOpts{configure: configure, shortTools: callOnceThen(fakeReply, fakeopenai.ToolCall{ID: "call_1", Name: "now", Arguments: "{}"})},
				[]string{"time"}, &recTool{name: "now"})
			img := env.putTestImage(t)
			env.setAppearance(t, testConvKey, img.ID, false)
			env.replyTo(t, false, "现在几点")
			reqs := env.short.Requests()
			if len(reqs) != 2 || !hasToolResult(reqs[1]) {
				t.Fatalf("requests = %d (tool result in the second: %v), want a tool round and a final answer", len(reqs), len(reqs) == 2 && hasToolResult(reqs[1]))
			}
			for i, req := range reqs {
				if image {
					assertImageSent(t, "round "+string(rune('0'+i)), req, img)
				} else {
					assertNoImage(t, "round "+string(rune('0'+i)), req)
				}
			}
		})
	}
}

func TestReplyImage_SkipFlag(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withImageModality("short_context")})
	img := env.putTestImage(t)
	env.setAppearance(t, testConvKey, img.ID, true)
	env.replyTo(t, false, "在吗")
	assertNoImage(t, "skip flag set", env.short.Requests()[0])
}

// imageFailStore fails every GetPersonaImage.
type imageFailStore struct{ store.Store }

func (imageFailStore) GetPersonaImage(context.Context, string, bool) (store.PersonaImage, error) {
	return store.PersonaImage{}, errors.New("test: images unavailable")
}

func TestReplyImage_MissingImageStillReplies(t *testing.T) {
	t.Run("never uploaded or purged", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: withImageModality("short_context")})
		sum := sha256.Sum256([]byte("an image that was never uploaded"))
		p := conloomapi.DefaultPersona()
		p.AppearanceImage = hex.EncodeToString(sum[:])
		if _, err := env.st.AppendPersonaVersion(context.Background(), store.PersonaVersion{
			ConversationKey: testConvKey, TemplateName: "default", Persona: p, Reason: "test", UpdatedBy: "test",
		}); err != nil {
			t.Fatal(err)
		}
		env.replyTo(t, false, "在吗")
		assertNoImage(t, "missing image", env.short.Requests()[0])
		if logs := env.logs.String(); !strings.Contains(logs, `msg="load appearance image failed; replying without it"`) {
			t.Errorf("logs lack the missing image warning:\n%s", logs)
		}
	})
	t.Run("read error", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: withImageModality("short_context")})
		img := env.putTestImage(t)
		env.setAppearance(t, testConvKey, img.ID, false)
		env.o.st = imageFailStore{env.o.st}
		env.replyTo(t, false, "在吗")
		assertNoImage(t, "read error", env.short.Requests()[0])
		if logs := env.logs.String(); !strings.Contains(logs, "test: images unavailable") {
			t.Errorf("logs lack the read error:\n%s", logs)
		}
	})
}

func TestReplyImage_SystemPromptUnchanged(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withImageModality("short_context")})
	img := env.putTestImage(t)
	env.setAppearance(t, testConvKey, "", false)
	env.replyTo(t, false, "在吗")
	env.setAppearance(t, testConvKey, img.ID, false)
	env.replyTo(t, false, "还在吗")
	reqs := env.short.Requests()
	assertNoImage(t, "without image", reqs[0])
	assertImageSent(t, "with image", reqs[1], img)
	before, _ := requestMessages(t, reqs[0])[0]["content"].(string)
	after, _ := requestMessages(t, reqs[1])[0]["content"].(string)
	if before == "" || after != before {
		t.Errorf("system prompt changed with the image:\n%s\nwant\n%s", after, before)
	}
}
