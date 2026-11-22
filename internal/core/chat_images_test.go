package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// chatImageShade makes every generated chat image differ from the others.
var chatImageShade atomic.Int32

// putChatImage stores a generated w×h PNG, processed like an upload, as an
// unsent chat image of convKey.
func (e *testEnv) putChatImage(t *testing.T, convKey string, w, h int) store.ChatImage {
	t.Helper()
	shade := uint8(chatImageShade.Add(37))
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			src.Set(x, y, color.RGBA{uint8(x), uint8(y), shade, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	c, err := imaging.ProcessChat(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img := store.ChatImage{ID: ids.New("img"), ConversationKey: convKey,
		ViewJPEG: c.ViewJPEG, ViewWidth: c.ViewWidth, ViewHeight: c.ViewHeight,
		ReplyJPEG: c.ReplyJPEG, ReplyWidth: c.ReplyWidth, ReplyHeight: c.ReplyHeight, ReplyTokens: c.ReplyTokens,
		CreatedBy: "admin"}
	if err := e.st.PutChatImage(context.Background(), img); err != nil {
		t.Fatal(err)
	}
	return img
}

func chatDataURL(img store.ChatImage) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img.ReplyJPEG)
}

// finalUserParts returns the parts of req's final user message in order: the
// text of a text part, the URL of an image part. A message sent as a string
// is one text part.
func finalUserParts(t *testing.T, req map[string]any) []string {
	t.Helper()
	msgs := requestMessages(t, req)
	i := len(msgs) - 1
	for i > 0 && msgs[i]["role"] != "user" {
		i--
	}
	last := msgs[i]
	if last["role"] != "user" {
		t.Fatalf("request has no user message: %v", msgs)
	}
	switch c := last["content"].(type) {
	case string:
		return []string{c}
	case []any:
		var out []string
		for _, p := range c {
			part, _ := p.(map[string]any)
			switch part["type"] {
			case "image_url":
				u, _ := part["image_url"].(map[string]any)
				url, _ := u["url"].(string)
				out = append(out, url)
			case "text":
				text, _ := part["text"].(string)
				out = append(out, text)
			default:
				t.Fatalf("unknown part %v", part)
			}
		}
		return out
	}
	t.Fatalf("final user message content = %T", last["content"])
	return nil
}

// chatImageParts returns the text and image parts of req's final user
// message, each in order.
func chatImageParts(t *testing.T, req map[string]any) (texts, urls []string) {
	t.Helper()
	for _, p := range finalUserParts(t, req) {
		if strings.HasPrefix(p, "data:image/") {
			urls = append(urls, p)
		} else {
			texts = append(texts, p)
		}
	}
	return texts, urls
}

// submitImages submits text with images in the panel chat convKey as admin.
func (e *testEnv) submitImages(t *testing.T, convKey, text string, images ...store.ChatImage) string {
	t.Helper()
	idList := make([]string, len(images))
	for i, img := range images {
		idList[i] = img.ID
	}
	id, _, err := e.o.SubmitPanel(context.Background(), convKey, "admin", text, idList...)
	if err != nil {
		t.Fatalf("SubmitPanel(%q, %v): %v", text, idList, err)
	}
	return id
}

// chatImageOf reads the chat image id of convKey.
func (e *testEnv) chatImageOf(t *testing.T, convKey, id string) store.ChatImage {
	t.Helper()
	got, err := e.st.GetChatImages(context.Background(), convKey, []string{id}, 0)
	if err != nil {
		t.Fatal(err)
	}
	img, ok := got[id]
	if !ok {
		t.Fatalf("chat image %s of %s not found", id, convKey)
	}
	return img
}

func imageAttachments(images ...store.ChatImage) []conloomapi.Attachment {
	out := []conloomapi.Attachment{}
	for _, img := range images {
		out = append(out, conloomapi.Attachment{Type: "image", ID: img.ID, MIME: "image/jpeg"})
	}
	return out
}

func TestSubmitPanel_Images(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	other := env.createChat(t, testBot)

	a := env.putChatImage(t, key, 40, 30)
	id := env.submitImages(t, key, "  ", a)
	msg := env.panelMessage(t, id)
	if msg.Text != "" || !slices.Equal(msg.Attachments, imageAttachments(a)) {
		t.Errorf("image-only message = text %q, attachments %+v; want \"\" and %+v", msg.Text, msg.Attachments, imageAttachments(a))
	}
	if got := env.chatImageOf(t, key, a.ID).MessageID; got != id {
		t.Errorf("image A claimed by %q, want %q", got, id)
	}
	env.waitTask(t, env.panelResponse(t, id).TaskID)

	b, c := env.putChatImage(t, key, 40, 30), env.putChatImage(t, key, 40, 30)
	id = env.submitImages(t, key, "看这两张", b, c)
	if msg := env.panelMessage(t, id); msg.Text != "看这两张" || !slices.Equal(msg.Attachments, imageAttachments(b, c)) {
		t.Errorf("text with 2 images = text %q, attachments %+v", msg.Text, msg.Attachments)
	}
	env.waitTask(t, env.panelResponse(t, id).TaskID)

	fresh := func() string { return env.putChatImage(t, key, 20, 20).ID }
	foreign := env.putChatImage(t, other, 20, 20).ID
	dup := fresh()
	for _, bad := range []struct {
		name   string
		images []string
	}{
		{"5 images", []string{fresh(), fresh(), fresh(), fresh(), fresh()}},
		{"duplicate", []string{dup, dup}},
		{"another chat's image", []string{fresh(), foreign}},
		{"already sent", []string{fresh(), a.ID}},
		{"unknown", []string{"img_unknown"}},
	} {
		before := env.chatIDs(t, key)
		_, _, err := env.o.SubmitPanel(ctx, key, "admin", "坏图", bad.images...)
		if !errors.Is(err, ErrBadImages) {
			t.Errorf("%s: err = %v, want ErrBadImages", bad.name, err)
		}
		if after := env.chatIDs(t, key); !slices.Equal(after, before) {
			t.Errorf("%s: messages %v after a refused submit, want %v", bad.name, after, before)
		}
		for _, id := range bad.images {
			got, err := env.st.GetChatImages(ctx, key, []string{id}, 0)
			if err != nil {
				t.Fatal(err)
			}
			if img, ok := got[id]; ok && id != a.ID && img.MessageID != "" {
				t.Errorf("%s: image %s claimed by %s", bad.name, id, img.MessageID)
			}
		}
	}

	if _, _, err := env.o.SubmitPanel(ctx, key, "admin", " \n "); !errors.Is(err, ErrBadText) {
		t.Errorf("blank text without images err = %v, want ErrBadText", err)
	}
	id = env.submitPanel(t, key, "admin", "只有字")
	if msg := env.panelMessage(t, id); msg.Attachments == nil || len(msg.Attachments) != 0 {
		t.Errorf("text-only attachments = %#v, want []", msg.Attachments)
	}
}

// purgeBeforeIngest purges every unsent chat image before an Ingest storing
// a message with attachments, as the cleanup loop may at any time.
type purgeBeforeIngest struct {
	store.Store
	t *testing.T
}

func (s purgeBeforeIngest) Ingest(ctx context.Context, in store.Ingest) (*store.EventRecord, error) {
	if in.Message != nil && len(in.Message.Attachments) > 0 {
		if n, err := s.PurgeChatImages(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 {
			s.t.Errorf("purge = %d, %v; want 1", n, err)
		}
	}
	return s.Store.Ingest(ctx, in)
}

// A purge between SubmitPanel's check and the store's claim fails the submit
// with ErrBadImages, not a store error.
func TestSubmitPanel_ImagePurgedBeforeClaim(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	a := env.putChatImage(t, key, 20, 20)
	env.o.st = purgeBeforeIngest{env.o.st, t}
	_, _, err := env.o.SubmitPanel(ctx, key, "admin", "你看", a.ID)
	if !errors.Is(err, ErrBadImages) {
		t.Fatalf("err = %v, want ErrBadImages", err)
	}
	if got := env.chatIDs(t, key); len(got) != 0 {
		t.Errorf("messages = %v, want none", got)
	}
}

// failAfterIngest stores what Ingest is given, then fails, as when the
// commit's answer is lost.
type failAfterIngest struct{ store.Store }

var errLostAck = errors.New("test: ingest answer lost")

func (s failAfterIngest) Ingest(ctx context.Context, in store.Ingest) (*store.EventRecord, error) {
	if _, err := s.Store.Ingest(ctx, in); err != nil {
		return nil, err
	}
	return nil, errLostAck
}

// A submit that fails after the store claimed its images for its own message
// keeps its error: the images are not gone, they went with the message.
func TestSubmitPanel_FailureAfterClaimNotBadImages(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	key := env.createChat(t, testBot)
	a := env.putChatImage(t, key, 20, 20)
	env.o.st = failAfterIngest{env.o.st}
	_, _, err := env.o.SubmitPanel(context.Background(), key, "admin", "你看", a.ID)
	if !errors.Is(err, errLostAck) || errors.Is(err, ErrBadImages) {
		t.Fatalf("err = %v, want the store's error, not ErrBadImages", err)
	}
	if got := env.chatImageOf(t, key, a.ID).MessageID; got == "" {
		t.Fatal("the image is not claimed: the test store did not store the message")
	}
}

func TestChatImages_ReplySeesImages(t *testing.T) {
	turn := func(withImages bool) (map[string]any, int, []store.ChatImage) {
		env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
			withImageModality("short_context")(c)
			allowAllPrivate(c)
		}})
		written := recordUsage(t, env)
		key := env.createChat(t, testBot)
		var images []store.ChatImage
		if withImages {
			images = []store.ChatImage{env.putChatImage(t, key, 300, 200), env.putChatImage(t, key, 1000, 600)}
		}
		task := env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", images...)).TaskID)
		if task.Status != conloomapi.TaskSucceeded {
			t.Fatalf("task = %+v", task)
		}
		calls := written()
		if len(calls) != 1 {
			t.Fatalf("recorded %d calls, want 1", len(calls))
		}
		return env.short.Requests()[0], calls[0].EstimatedTokens, images
	}
	_, plain, _ := turn(false)
	req, got, images := turn(true)
	a, b := images[0], images[1]
	parts := finalUserParts(t, req)
	want := []string{contextbuilder.ChatImageLabel(1), chatDataURL(a), contextbuilder.ChatImageLabel(2), chatDataURL(b)}
	if len(parts) != 5 || !slices.Equal(parts[:4], want) {
		t.Fatalf("final user parts = %.200q, want the labelled images then the text", parts)
	}
	if !strings.Contains(parts[4], "你看[图片1][图片2]") {
		t.Errorf("text = %q, want it to contain 你看[图片1][图片2]", parts[4])
	}
	if urls, _ := requestImages(t, req); len(urls) != 2 {
		t.Errorf("request carries %d images, want 2", len(urls))
	}
	if got < plain+a.ReplyTokens+b.ReplyTokens {
		t.Errorf("estimated tokens = %d, want at least %d + %d + %d", got, plain, a.ReplyTokens, b.ReplyTokens)
	}
}

func TestChatImages_ReplyDropsImageLabels(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{
		short: answer(contextbuilder.ChatImageLabel(1) + "\n[图片1]是猫[笑]，[图片2] 是狗（摸摸头）"),
		configure: func(c *config.Config) {
			withImageModality("short_context")(c)
			allowAllPrivate(c)
		},
	})
	key := env.createChat(t, testBot)
	a, b := env.putChatImage(t, key, 60, 40), env.putChatImage(t, key, 60, 40)
	task := env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a, b)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	if got, want := task.Actions[0].Content.Text, "是猫[笑]，是狗（摸摸头）"; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestChatImages_WithAppearanceInFirstMessage(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
	}})
	key := env.createChat(t, testBot)
	look := env.putTestImage(t)
	env.setAppearance(t, key, look.ID, false)
	a := env.putChatImage(t, key, 300, 200)
	env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a)).TaskID)
	req := env.short.Requests()[0]
	if msgs := requestMessages(t, req); len(msgs) != 2 {
		t.Fatalf("messages = %d, want the system prompt and one user message", len(msgs))
	}
	parts := finalUserParts(t, req)
	want := []string{contextbuilder.AppearanceLabel, dataURL(look), contextbuilder.ChatImageLabel(1), chatDataURL(a)}
	if len(parts) != 5 || !slices.Equal(parts[:4], want) {
		t.Fatalf("user parts = %.200q, want the appearance, then the chat image, each after its label", parts)
	}
	if !strings.Contains(parts[4], "你看[图片1]") || strings.Contains(parts[4], contextbuilder.AppearanceLabel) {
		t.Errorf("text = %q, want 你看[图片1] and no appearance label", parts[4])
	}
}

func TestChatImages_HistoryNeverResent(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
	}})
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	a, b := env.putChatImage(t, key, 300, 200), env.putChatImage(t, key, 200, 300)
	id := env.submitImages(t, key, "你看", a, b)
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	env.waitPanelReply(t, d, key, env.panelMessage(t, id).Seq)

	env.waitTask(t, env.panelResponse(t, env.submitPanel(t, key, "admin", "然后呢")).TaskID)
	reqs := env.short.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	assertNoImage(t, "next turn", reqs[1])
	text := requestText(t, reqs[1])
	if !strings.Contains(text, "你看"+seenEarlier+seenEarlier) || strings.Contains(text, "[图片1]") {
		t.Errorf("next turn text = %q, want the earlier message as 你看 and two images seen earlier", text)
	}
}

func TestChatImages_HistoryUsesCachedDescription(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
	}})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	a, b := env.putChatImage(t, key, 300, 200), env.putChatImage(t, key, 200, 300)
	id := env.submitImages(t, key, "你看", a, b)
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	env.waitPanelReply(t, d, key, env.panelMessage(t, id).Seq)
	if err := env.st.SetChatImageDescription(ctx, key, b.ID, "一只橘猫趴在窗台上"); err != nil {
		t.Fatal(err)
	}

	env.waitTask(t, env.panelResponse(t, env.submitPanel(t, key, "admin", "然后呢")).TaskID)
	req := env.short.Requests()[1]
	assertNoImage(t, "next turn", req)
	if text := requestText(t, req); !strings.Contains(text, "你看"+seenEarlier+"[图片：一只橘猫趴在窗台上]") {
		t.Errorf("next turn text = %q, want the described image in the history", text)
	}
}

func TestChatImages_MergedTurnKeepsNewestFour(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		privateWait(300 * time.Millisecond)(c)
	}})
	key := env.createChat(t, testBot)
	var img []store.ChatImage
	for range 5 {
		img = append(img, env.putChatImage(t, key, 60, 40))
	}
	m1 := env.submitImages(t, key, "", img[0], img[1], img[2])
	m2 := env.submitImages(t, key, "", img[3], img[4])
	taskID := env.panelResponse(t, m1).TaskID
	if got := env.panelResponse(t, m2).TaskID; got != taskID {
		t.Fatalf("m2 went to task %q, want it merged into %q", got, taskID)
	}
	env.waitTask(t, taskID)
	reqs := env.short.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	texts, urls := chatImageParts(t, reqs[0])
	wantURLs := []string{chatDataURL(img[1]), chatDataURL(img[2]), chatDataURL(img[3]), chatDataURL(img[4])}
	if !slices.Equal(urls, wantURLs) {
		t.Errorf("sent images differ from B, C, D, E (%d sent)", len(urls))
	}
	wantLabels := []string{contextbuilder.ChatImageLabel(1), contextbuilder.ChatImageLabel(2),
		contextbuilder.ChatImageLabel(3), contextbuilder.ChatImageLabel(4)}
	if len(texts) != 5 || !slices.Equal(texts[:4], wantLabels) {
		t.Fatalf("text parts = %q, want 4 labels and the text", texts)
	}
	const only = "（对方这条只发了图片，没有文字）"
	if body := texts[4]; !strings.Contains(body, "[图片][图片1][图片2]"+only+"\n") || !strings.Contains(body, "[图片3][图片4]"+only) {
		t.Errorf("text = %q, want m1 as [图片][图片1][图片2] and m2 as [图片3][图片4], each marked image-only", body)
	}
}

func TestChatImages_RegenerateKeepsImages(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
	}})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	a, b := env.putChatImage(t, key, 300, 200), env.putChatImage(t, key, 200, 300)
	id := env.submitImages(t, key, "你看", a, b)
	env.waitTask(t, env.panelResponse(t, id).TaskID)
	reply := env.waitPanelReply(t, d, key, env.panelMessage(t, id).Seq)

	newID, err := env.o.RegeneratePanel(ctx, key, reply.Seq)
	if err != nil {
		t.Fatalf("RegeneratePanel: %v", err)
	}
	env.waitTask(t, newID)
	reqs := env.short.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	first, again := finalUserParts(t, reqs[0]), finalUserParts(t, reqs[1])
	want := []string{contextbuilder.ChatImageLabel(1), chatDataURL(a), contextbuilder.ChatImageLabel(2), chatDataURL(b)}
	if len(first) != 5 || len(again) != 5 || !slices.Equal(again[:4], want) || !slices.Equal(first[:4], again[:4]) {
		t.Fatalf("regenerated parts = %.200q, want the same labelled images as the first reply", again)
	}
	if !strings.Contains(again[4], "你看[图片1][图片2]") {
		t.Errorf("regenerated text = %q", again[4])
	}
}

// The state step is text only: the turn's image renders as seen earlier when
// the reply model took it, else as [图片].
func TestChatImages_StateStepTextOnly(t *testing.T) {
	for _, seen := range []bool{true, false} {
		t.Run(map[bool]string{true: "seen by the reply", false: "text-only reply"}[seen], func(t *testing.T) {
			configure := func(*config.Config) {}
			want := "你看" + seenEarlier
			if seen {
				configure = withImageModality("short_context")
			} else {
				want = "你看[图片]"
			}
			env, _, _ := stepEnv(t, testOpts{
				configure:  configure,
				shortTools: stateModel(t, textReply("好看")),
			}, config.StateStepAfterReply)
			key := env.createChat(t, testBot)
			a := env.putChatImage(t, key, 300, 200)
			env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a)).TaskID)
			isState := func(req map[string]any) bool {
				texts, _ := chatImageParts(t, req)
				return strings.Contains(strings.Join(texts, "\n"), "【状态更新】")
			}
			waitUntil(t, "a state step request", func() bool { return slices.ContainsFunc(env.short.Requests(), isState) })
			waitStateIdle(t, env)

			var replies, states int
			for _, req := range env.short.Requests() {
				if isState(req) {
					states++
					assertNoImage(t, "state step", req)
					last := lastUserText(t, req)
					if !strings.Contains(last, want) || strings.Contains(last, "[图片1]") {
						t.Errorf("state step text = %q, want %s and no [图片1]", last, want)
					}
					if seen && strings.Contains(last, "你看[图片]") {
						t.Errorf("state step text = %q shows the seen image as [图片]", last)
					}
					continue
				}
				replies++
				if _, urls := chatImageParts(t, req); seen != (len(urls) == 1) {
					t.Errorf("reply request carries %d images", len(urls))
				}
			}
			if replies == 0 || states == 0 {
				t.Fatalf("requests: %d replies, %d state steps; want both", replies, states)
			}
		})
	}
}

// chatImageFailStore fails every GetChatImages that reads reply copies.
type chatImageFailStore struct{ store.Store }

func (s chatImageFailStore) GetChatImages(ctx context.Context, convKey string, idList []string, data store.ChatImageData) (map[string]store.ChatImage, error) {
	if data&store.ChatImageReply != 0 {
		return nil, errors.New("test: chat images unavailable")
	}
	return s.Store.GetChatImages(ctx, convKey, idList, data)
}

func TestChatImages_UnreadableImageLeftOut(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
	}})
	key := env.createChat(t, testBot)
	a := env.putChatImage(t, key, 300, 200)
	env.o.st = chatImageFailStore{env.o.st}
	task := env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) == 0 {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
	req := env.short.Requests()[0]
	assertNoImage(t, "unreadable image", req)
	if text := lastUserText(t, req); !strings.Contains(text, "你看[图片]") {
		t.Errorf("text = %q, want 你看[图片]", text)
	}
	if logs := env.logs.String(); !strings.Contains(logs, "test: chat images unavailable") {
		t.Errorf("logs lack the read error:\n%s", logs)
	}
}
