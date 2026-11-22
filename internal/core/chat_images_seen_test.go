package core

import (
	"context"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// seenEarlier is how history shows an image a reply model took itself and
// that has no description.
const seenEarlier = "[图片（你之前看过，细节以你当时的回复为准）]"

// seeingEnv has short_context take images; vision, when set, answers vision
// requests, and sensitive says whether it may see private chats.
func seeingEnv(t *testing.T, vision handlerFunc, sensitive bool) *testEnv {
	t.Helper()
	return newTestOrchestrator(t, testOpts{vision: vision, configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
		if p, ok := c.ModelProfiles["vision"]; ok {
			p.Privacy.AllowSensitiveContent = sensitive
			c.ModelProfiles["vision"] = p
		}
	}})
}

// nextTurnText submits 然后呢 in key, waits for its task and returns the
// text of the newest short_context request.
func nextTurnText(t *testing.T, env *testEnv, key string) string {
	t.Helper()
	env.waitTask(t, env.panelResponse(t, env.submitPanel(t, key, "admin", "然后呢")).TaskID)
	reqs := env.short.Requests()
	assertNoImage(t, "next turn", reqs[len(reqs)-1])
	return requestText(t, reqs[len(reqs)-1])
}

// A reply that saw an image has it described in the background, and later
// turns show the description.
func TestChatImages_SeenDescribedInBackground(t *testing.T) {
	env := seeingEnv(t, answer(catDescription), true)
	written := recordUsage(t, env)
	key := env.createChat(t, testBot)
	d := env.startPanelDeliverer(t)
	a := env.putChatImage(t, key, 1600, 800)
	id := env.submitImages(t, key, "你看", a)
	first := env.waitTask(t, env.panelResponse(t, id).TaskID)
	env.waitPanelReply(t, d, key, env.panelMessage(t, id).Seq)
	waitUntil(t, "the background description", func() bool {
		return env.chatImageOf(t, key, a.ID).Description == catDescription
	})
	vreqs := env.vision.Requests()
	if len(vreqs) != 1 {
		t.Fatalf("vision requests = %d, want 1", len(vreqs))
	}
	if parts := finalUserParts(t, vreqs[0]); len(parts) != 2 || parts[0] != viewDataURL(a) || parts[1] != chatDescribePrompt {
		t.Errorf("vision request parts = %.120q, want the view copy and chatDescribePrompt", parts)
	}
	if !env.chatImageOf(t, key, a.ID).Seen {
		t.Error("the image is not marked seen")
	}

	if text := nextTurnText(t, env, key); !strings.Contains(text, "你看[图片："+catDescription+"]") {
		t.Errorf("next turn text = %q, want the description in the history", text)
	}
	if n := env.vision.Calls(); n != 1 {
		t.Errorf("vision calls = %d, want 1", n)
	}
	firstTask := first.TaskID
	var vision []store.ModelCall
	for _, c := range written() {
		if c.Purpose == store.PurposeVision {
			vision = append(vision, c)
		}
	}
	if len(vision) != 1 || vision[0].TaskID != firstTask || vision[0].ConversationKey != key ||
		vision[0].BotInstanceID != testBot || vision[0].Outcome != store.OutcomeOK {
		t.Errorf("vision calls = %+v, want one under task %s of %s", vision, firstTask, key)
	}
}

// Without a description, an image the reply model saw shows as seen earlier,
// never as the cannot-see [图片].
func TestChatImages_SeenEarlierWithoutDescription(t *testing.T) {
	for _, tc := range []struct {
		name   string
		vision handlerFunc
	}{
		{"no vision profile", nil},
		{"vision not allowed in private chats", answer(catDescription)},
		{"vision fails", failModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := seeingEnv(t, tc.vision, tc.name == "vision fails")
			key, a, _ := firstImageTurn(t, env)
			if tc.name == "vision fails" {
				waitUntil(t, "the failed background description", func() bool {
					return strings.Contains(env.logs.String(), `reason="vision call failed"`)
				})
			}
			text := nextTurnText(t, env, key)
			if !strings.Contains(text, "你看"+seenEarlier) || strings.Contains(text, "你看[图片]") {
				t.Errorf("next turn text = %q, want 你看%s", text, seenEarlier)
			}
			if got := env.chatImageOf(t, key, a.ID); !got.Seen || got.Description != "" {
				t.Errorf("image = seen %v, description %q; want seen and none", got.Seen, got.Description)
			}
			if tc.vision != nil && tc.name != "vision fails" && env.vision.Calls() != 0 {
				t.Errorf("vision calls = %d, want 0", env.vision.Calls())
			}
		})
	}
}

// An image a text-only reply could not see stays [图片] in later turns.
func TestChatImages_UnseenStaysPlain(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	key, a, _ := firstImageTurn(t, env)
	if text := nextTurnText(t, env, key); !strings.Contains(text, "你看[图片]") || strings.Contains(text, seenEarlier) {
		t.Errorf("next turn text = %q, want 你看[图片]", text)
	}
	if env.chatImageOf(t, key, a.ID).Seen {
		t.Error("an image no model saw is marked seen")
	}
}

// The background description neither delays the reply nor outlives Close.
func TestChatImages_BackgroundDescribeCancelledByClose(t *testing.T) {
	release := make(chan struct{})
	env := seeingEnv(t, func(map[string]any) (int, string) {
		<-release
		return 200, catDescription
	}, true)
	t.Cleanup(func() { close(release) })
	key, a, _ := firstImageTurn(t, env)
	waitUntil(t, "the background vision request", func() bool { return env.vision.Calls() == 1 })

	closed := make(chan struct{})
	go func() {
		env.o.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(waitLimit):
		t.Fatal("Close did not return while a background description was running")
	}
	if got := env.chatImageOf(t, key, a.ID); got.Description != "" || !got.Seen {
		t.Errorf("image = seen %v, description %q; want seen and none", got.Seen, got.Description)
	}
	if logs := env.logs.String(); !strings.Contains(logs, `reason="vision call failed"`) {
		t.Errorf("logs lack the cancelled description:\n%s", logs)
	}
}

// Once the describe deadline passed, the images left are not sent to the
// vision profile; one line logs them.
func TestChatImages_DescribeStopsAtDeadline(t *testing.T) {
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{vision: func(map[string]any) (int, string) {
		<-release
		return 200, catDescription
	}, configure: allowAllPrivate})
	t.Cleanup(func() { close(release) })
	env.o.chatDescribeTimeout = 200 * time.Millisecond
	key := env.createChat(t, testBot)
	a, b, c := env.putChatImage(t, key, 60, 40), env.putChatImage(t, key, 60, 40), env.putChatImage(t, key, 60, 40)
	task := env.waitTask(t, env.panelResponse(t, env.submitImages(t, key, "你看", a, b, c)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) == 0 {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
	if n := env.vision.Calls(); n != 1 {
		t.Errorf("vision calls = %d, want 1: none after the deadline", n)
	}
	if text := lastUserText(t, env.short.Requests()[0]); !strings.Contains(text, "你看"+strings.Repeat("[图片（看不到内容）]", 3)) {
		t.Errorf("text = %q, want three cannot-see images", text)
	}
	logs := env.logs.String()
	late := regexp.MustCompile(`(?m)^.*reason="out of time".*$`).FindAllString(logs, -1)
	if len(late) != 1 || !strings.Contains(late[0], b.ID) || !strings.Contains(late[0], c.ID) || strings.Contains(late[0], a.ID) {
		t.Errorf("out-of-time log lines = %q, want one naming %s and %s", late, b.ID, c.ID)
	}
	if n := strings.Count(logs, `msg="chat image not described"`); n != 2 {
		t.Errorf("not-described log lines = %d, want 2 (the failed call, the rest)", n)
	}
}

// The admission lease of a background description is at background priority:
// an interactive request that comes later is served first.
func TestChatImages_BackgroundDescribePriority(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{vision: answer(catDescription), configure: func(c *config.Config) {
		withImageModality("short_context")(c)
		allowAllPrivate(c)
		p := c.ModelProfiles["vision"]
		p.Runtime.MaxConcurrency = 1
		c.ModelProfiles["vision"] = p
	}})
	ctx := context.Background()
	lease := func(id string) *admission.Lease {
		l, err := env.o.adm.Acquire(ctx, admission.Request{ID: id, Profile: "vision", Tokens: 1,
			Priority: admission.PriorityInteractive, TTL: time.Minute})
		if err != nil {
			t.Errorf("Acquire %s: %v", id, err)
		}
		return l
	}
	held := lease("held")
	key, a, _ := firstImageTurn(t, env)
	waiting := func(n int) func() bool {
		return func() bool { return env.o.adm.Stats("vision").Waiting == n }
	}
	waitUntil(t, "the background description to wait", waiting(1))
	got := make(chan *admission.Lease, 1)
	go func() { got <- lease("interactive") }()
	waitUntil(t, "the interactive request to wait", waiting(2))
	held.Release()
	var interactive *admission.Lease
	select {
	case interactive = <-got:
	case <-time.After(waitLimit):
		t.Fatal("the interactive request was not served")
	}
	if n := env.vision.Calls(); n != 0 {
		t.Errorf("vision calls = %d before the interactive lease was released, want 0", n)
	}
	interactive.Release()
	waitUntil(t, "the background description", func() bool {
		return env.chatImageOf(t, key, a.ID).Description == catDescription
	})
}

// A background description skips an image withdrawn, or whose chat was
// deleted, while an earlier image of the turn was being described.
func TestChatImages_BackgroundDescribeSkipsRemovedImages(t *testing.T) {
	for _, remove := range []string{"withdraw", "delete chat"} {
		t.Run(remove, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			env := seeingEnv(t, func(map[string]any) (int, string) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				return 200, catDescription
			}, true)
			ctx := context.Background()
			key := env.createChat(t, testBot)
			d := env.startPanelDeliverer(t)
			first, b := env.putChatImage(t, key, 60, 40), env.putChatImage(t, key, 60, 40)
			id := env.submitImages(t, key, "你看", first, b)
			env.waitTask(t, env.panelResponse(t, id).TaskID)
			env.waitPanelReply(t, d, key, env.panelMessage(t, id).Seq)
			select {
			case <-entered:
			case <-time.After(waitLimit):
				t.Fatal("no background vision request")
			}
			var err error
			if remove == "withdraw" {
				_, err = env.o.WithdrawPanel(ctx, key, env.panelMessage(t, id).Seq)
			} else {
				err = env.o.DeletePanelChat(ctx, key)
			}
			if err != nil {
				t.Fatalf("%s: %v", remove, err)
			}
			close(release)
			waitUntil(t, "the skipped image", func() bool {
				return strings.Contains(env.logs.String(), `reason="image removed"`)
			})
			env.o.Close()
			if n := env.vision.Calls(); n != 1 {
				t.Errorf("vision calls = %d, want 1: the removed image is not described", n)
			}
			if logs := env.logs.String(); !regexp.MustCompile(`level=INFO msg="chat image not described".*reason="image removed" image=` + b.ID).MatchString(logs) {
				t.Errorf("logs lack the Info skip of %s:\n%s", b.ID, logs)
			}
		})
	}
}
