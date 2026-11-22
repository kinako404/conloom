package core

import (
	"context"
	"encoding/base64"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	catDescription  = "一只橘猫趴在键盘上"
	unseenImageNote = "（标着“看不到内容”的图片你没有收到，不知道里面是什么，不要假装看到了。）"
)

var seenPlaceholder = regexp.MustCompile(`\[图片\d`)

// requestAllText returns every text of req's messages, string contents and
// text parts alike.
func requestAllText(t *testing.T, req map[string]any) string {
	t.Helper()
	var all strings.Builder
	for _, m := range requestMessages(t, req) {
		switch c := m["content"].(type) {
		case string:
			all.WriteString(c + "\n")
		case []any:
			for _, p := range c {
				part, _ := p.(map[string]any)
				if text, ok := part["text"].(string); ok {
					all.WriteString(text + "\n")
				}
			}
		}
	}
	return all.String()
}

// assertTextOnly checks that req, sent to a text-only profile, carries no
// image and no [图片N] placeholder of a seen image.
func assertTextOnly(t *testing.T, what string, req map[string]any) {
	t.Helper()
	assertNoImage(t, what, req)
	for _, m := range requestMessages(t, req) {
		if _, ok := m["content"].(string); !ok && m["content"] != nil {
			t.Errorf("%s: a message is sent as parts: %.200v", what, m["content"])
		}
	}
	if text := requestAllText(t, req); seenPlaceholder.MatchString(text) {
		t.Errorf("%s: request text has a seen-image placeholder:\n%s", what, text)
	}
}

func viewDataURL(img store.ChatImage) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img.ViewJPEG)
}

func failModel(map[string]any) (int, string) { return 500, "boom" }

// reRouteEnv has short_context take images and always fail, so every turn
// goes on to the text-only long_context, and a vision profile allowing
// sensitive content that answers catDescription.
func reRouteEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestOrchestrator(t, testOpts{short: failModel, vision: answer(catDescription), configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
	}})
}

// firstImageTurn submits 你看 with a 1600×800 image in a new chat of env and
// waits for the reply to be delivered.
func firstImageTurn(t *testing.T, env *testEnv) (key string, img store.ChatImage, reply store.MessageRecord) {
	t.Helper()
	key = env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	img = env.putChatImage(t, key, 1600, 800)
	id := env.submitImages(t, key, "你看", img)
	task := env.waitTask(t, env.panelResponse(t, id).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) == 0 {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
	return key, img, env.waitPanelReply(t, d, key, env.panelMessage(t, id).Seq)
}

func TestChatImages_ReRouteToTextOnlyDescribes(t *testing.T) {
	env := reRouteEnv(t)
	written := recordUsage(t, env)
	key, a, _ := firstImageTurn(t, env)

	if reqs := env.short.Requests(); len(reqs) != 1 {
		t.Fatalf("short requests = %d, want 1", len(reqs))
	} else if _, urls := chatImageParts(t, reqs[0]); len(urls) != 1 || urls[0] != chatDataURL(a) {
		t.Errorf("short_context got %d images, want the reply copy", len(urls))
	}

	vreqs := env.vision.Requests()
	if len(vreqs) != 1 {
		t.Fatalf("vision requests = %d, want 1", len(vreqs))
	}
	if a.ViewWidth != 1536 || a.ViewHeight != 768 {
		t.Fatalf("view copy = %dx%d, want 1536x768", a.ViewWidth, a.ViewHeight)
	}
	vparts := finalUserParts(t, vreqs[0])
	if len(vparts) != 2 || vparts[0] != viewDataURL(a) || vparts[1] != chatDescribePrompt {
		t.Errorf("vision request parts = %.120q, want the view copy and chatDescribePrompt", vparts)
	}

	lreqs := env.long.Requests()
	if len(lreqs) != 1 {
		t.Fatalf("long requests = %d, want 1", len(lreqs))
	}
	assertTextOnly(t, "long_context", lreqs[0])
	if text := lastUserText(t, lreqs[0]); !strings.Contains(text, "你看[图片："+catDescription+"]") || strings.Contains(text, unseenImageNote) {
		t.Errorf("long_context text = %q, want 你看[图片：%s] and no unseen note", text, catDescription)
	}
	if got := env.chatImageOf(t, key, a.ID).Description; got != catDescription {
		t.Errorf("cached description = %q, want %q", got, catDescription)
	}

	env.waitTask(t, env.panelResponse(t, env.submitPanel(t, key, "admin", "然后呢")).TaskID)
	lreqs = env.long.Requests()
	if len(lreqs) != 2 {
		t.Fatalf("long requests = %d, want 2", len(lreqs))
	}
	assertTextOnly(t, "next turn", lreqs[1])
	if text := requestText(t, lreqs[1]); !strings.Contains(text, "你看[图片："+catDescription+"]") {
		t.Errorf("next turn text = %q, want the history to show the description", text)
	}
	if n := env.vision.Calls(); n != 1 {
		t.Errorf("vision calls = %d, want 1", n)
	}

	var vision int
	for _, c := range written() {
		if c.Purpose == store.PurposeVision {
			vision++
			if c.Role != "vision" || c.Outcome != store.OutcomeOK {
				t.Errorf("vision call = %+v", c)
			}
		}
	}
	if vision != 1 {
		t.Errorf("recorded %d vision calls, want 1", vision)
	}
	logs := env.logs.String()
	if !strings.Contains(logs, `msg="chat image described"`) || !strings.Contains(logs, a.ID) {
		t.Errorf("logs lack the description record:\n%s", logs)
	}
	if strings.Contains(logs, "base64,") {
		t.Errorf("logs carry image data:\n%.500s", logs)
	}
}

func TestChatImages_CachedDescriptionNotAskedAgain(t *testing.T) {
	env := reRouteEnv(t)
	key, a, reply := firstImageTurn(t, env)
	newID, err := env.o.RegeneratePanel(context.Background(), key, reply.Seq)
	if err != nil {
		t.Fatalf("RegeneratePanel: %v", err)
	}
	env.waitTask(t, newID)
	lreqs := env.long.Requests()
	if len(lreqs) != 2 {
		t.Fatalf("long requests = %d, want 2", len(lreqs))
	}
	if sreqs := env.short.Requests(); len(sreqs) != 2 {
		t.Fatalf("short requests = %d, want 2", len(sreqs))
	} else if _, urls := chatImageParts(t, sreqs[1]); len(urls) != 1 || urls[0] != chatDataURL(a) {
		t.Errorf("regenerated short_context request carries %d images, want the image again", len(urls))
	}
	assertTextOnly(t, "regenerated long_context", lreqs[1])
	if text := lastUserText(t, lreqs[1]); !strings.Contains(text, "你看[图片："+catDescription+"]") {
		t.Errorf("regenerated text = %q, want the cached description", text)
	}
	if n := env.vision.Calls(); n != 1 {
		t.Errorf("vision calls = %d, want 1: the description is cached", n)
	}
}

// assertCannotSee checks that req's current text shows a single image as
// cannot-see, with the note.
func assertCannotSee(t *testing.T, what string, req map[string]any) {
	t.Helper()
	assertTextOnly(t, what, req)
	text := lastUserText(t, req)
	if !strings.Contains(text, "你看[图片（看不到内容）]") || strings.Count(text, unseenImageNote) != 1 {
		t.Errorf("%s: text = %q, want 你看[图片（看不到内容）] and the note once", what, text)
	}
}

func TestChatImages_NoVisionCannotSee(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	key := env.createChat(t, testBot)
	a := env.putChatImage(t, key, 300, 200)
	task := env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) == 0 {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
	reqs := env.short.Requests()
	if len(reqs) != 1 {
		t.Fatalf("short requests = %d, want 1", len(reqs))
	}
	assertCannotSee(t, "no vision", reqs[0])
	if logs := env.logs.String(); !strings.Contains(logs, `msg="chat image not described"`) || !strings.Contains(logs, a.ID) {
		t.Errorf("logs lack the not-described record:\n%s", logs)
	}
}

func TestChatImages_VisionNotAllowedSensitive(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{vision: answer(catDescription), configure: func(c *config.Config) {
		allowAllPrivate(c)
		p := c.ModelProfiles["vision"]
		p.Privacy.AllowSensitiveContent = false
		c.ModelProfiles["vision"] = p
	}})
	key := env.createChat(t, testBot)
	a := env.putChatImage(t, key, 300, 200)
	task := env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) == 0 {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
	if n := env.vision.Calls(); n != 0 {
		t.Errorf("vision calls = %d, want 0: it may not see private chats", n)
	}
	assertCannotSee(t, "vision not allowed", env.short.Requests()[0])
	if got := env.chatImageOf(t, key, a.ID).Description; got != "" {
		t.Errorf("cached description = %q, want none", got)
	}
}

func TestChatImages_VisionFailsCannotSee(t *testing.T) {
	for _, tc := range []struct {
		name   string
		vision func(release <-chan struct{}) handlerFunc
	}{
		{"500", func(<-chan struct{}) handlerFunc { return failModel }},
		{"late", func(release <-chan struct{}) handlerFunc {
			return func(map[string]any) (int, string) {
				<-release
				return 200, catDescription
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			env := newTestOrchestrator(t, testOpts{vision: tc.vision(release), configure: allowAllPrivate})
			t.Cleanup(func() { close(release) })
			if env.o.chatDescribeTimeout != 30*time.Second {
				t.Errorf("chat describe timeout = %v, want 30s", env.o.chatDescribeTimeout)
			}
			env.o.chatDescribeTimeout = 200 * time.Millisecond
			key := env.createChat(t, testBot)
			a := env.putChatImage(t, key, 300, 200)
			task := env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a)).TaskID)
			if task.Status != conloomapi.TaskSucceeded || len(task.Actions) == 0 {
				t.Fatalf("task = %+v, want the reply sent", task)
			}
			if n := env.vision.Calls(); n != 1 {
				t.Errorf("vision calls = %d, want 1", n)
			}
			assertCannotSee(t, tc.name, env.short.Requests()[0])
			if got := env.chatImageOf(t, key, a.ID).Description; got != "" {
				t.Errorf("cached description = %q, want none after a failure", got)
			}
			if logs := env.logs.String(); !strings.Contains(logs, `msg="chat image not described"`) {
				t.Errorf("logs lack the not-described record:\n%s", logs)
			}
		})
	}
}

func TestChatImages_TooLargeForImageProfile(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{vision: answer(catDescription), configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
	}})
	key := env.createChat(t, testBot)
	var images []store.ChatImage
	for range 4 {
		images = append(images, env.putChatImage(t, key, 768, 768))
	}
	task := env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", images...)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) == 0 {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
	if n := env.short.Calls(); n != 0 {
		t.Errorf("short calls = %d, want 0: four images do not fit it", n)
	}
	lreqs := env.long.Requests()
	if len(lreqs) != 1 {
		t.Fatalf("long requests = %d, want 1", len(lreqs))
	}
	assertTextOnly(t, "long_context", lreqs[0])
	described := "[图片：" + catDescription + "]"
	if text := lastUserText(t, lreqs[0]); !strings.Contains(text, "你看"+strings.Repeat(described, 4)) {
		t.Errorf("long_context text = %q, want four described images", text)
	}
	if n := env.vision.Calls(); n != 4 {
		t.Errorf("vision calls = %d, want one per image", n)
	}
}

func TestSuggestTranscript_Images(t *testing.T) {
	image := func(id string) conloomapi.Attachment {
		return conloomapi.Attachment{Type: "image", ID: id, MIME: "image/jpeg"}
	}
	msgs := []store.MessageRecord{
		{DisplayName: "小明", Text: "看", Attachments: []conloomapi.Attachment{image("img_a")}},
		{DisplayName: "小明", Attachments: []conloomapi.Attachment{image("img_b"), image("img_c")}},
	}
	if got, want := suggestTranscript(msgs, "小红"), "小明：看[图片]\n小明：[图片][图片]"; got != want {
		t.Errorf("suggestTranscript = %q, want %q", got, want)
	}
}

func TestChatImages_StateStepUsesDescriptions(t *testing.T) {
	for _, seen := range []bool{false, true} {
		t.Run(map[bool]string{false: "described for a text-only reply", true: "cached, seen by the reply"}[seen], func(t *testing.T) {
			configure := func(*config.Config) {}
			if seen {
				configure = withImageModality("short_context")
			}
			env, _, _ := stepEnv(t, testOpts{
				configure:  configure,
				vision:     answer(catDescription),
				shortTools: stateModel(t, textReply("好看")),
			}, config.StateStepAfterReply)
			key := env.createChat(t, testBot)
			a := env.putChatImage(t, key, 300, 200)
			if seen {
				if err := env.st.SetChatImageDescription(context.Background(), key, a.ID, catDescription); err != nil {
					t.Fatal(err)
				}
			}
			env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a)).TaskID)
			isState := func(req map[string]any) bool {
				texts, _ := chatImageParts(t, req)
				return strings.Contains(strings.Join(texts, "\n"), "【状态更新】")
			}
			waitUntil(t, "a state step request", func() bool { return slices.ContainsFunc(env.short.Requests(), isState) })
			waitStateIdle(t, env)

			var states int
			for _, req := range env.short.Requests() {
				if !isState(req) {
					if _, urls := chatImageParts(t, req); seen != (len(urls) == 1) {
						t.Errorf("reply request carries %d images", len(urls))
					}
					continue
				}
				states++
				assertTextOnly(t, "state step", req)
				if last := lastUserText(t, req); !strings.Contains(last, "你看[图片："+catDescription+"]") {
					t.Errorf("state step text = %q, want the description", last)
				}
			}
			if states == 0 {
				t.Fatal("no state step request")
			}
			if n := env.vision.Calls(); n != map[bool]int{false: 1, true: 0}[seen] {
				t.Errorf("vision calls = %d", n)
			}
		})
	}
}

func TestChatDescription_OneLineBracketsCut(t *testing.T) {
	if got, want := chatDescription("  一只猫\n趴在 [键盘] 上  "), "一只猫 趴在 ［键盘］ 上"; got != want {
		t.Errorf("chatDescription = %q, want %q", got, want)
	}
	if got := chatDescription(strings.Repeat("猫", chatDescriptionMaxRunes+10)); got != strings.Repeat("猫", chatDescriptionMaxRunes) {
		t.Errorf("long description kept %d characters, want %d", len([]rune(got)), chatDescriptionMaxRunes)
	}
}
