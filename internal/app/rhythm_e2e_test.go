package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/panel"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// adminChat is the private chat with admin, the only one private_chat: admins allows.
var adminChat = mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: admin.PlatformID}

// rhythmConfig is testConfig whose private chats wait for a pause of wait and
// split replies into at most maxParts messages.
func rhythmConfig(t *testing.T, modelURL string, wait time.Duration, maxParts int) *config.Config {
	t.Helper()
	cfg := testConfig(t, configParams{modelURL: modelURL})
	setPrivateRhythm(t, cfg, wait, maxParts)
	return cfg
}

// setPrivateRhythm gives cfg a default private policy that waits for a pause
// of wait and splits replies into at most maxParts messages.
func setPrivateRhythm(t *testing.T, cfg *config.Config, wait time.Duration, maxParts int) {
	t.Helper()
	private := cfg.DefaultGroupPolicy
	private.Reply.PrivateWait = conloomapi.Duration{Duration: wait}
	private.Reply.PrivateMaxWait = conloomapi.Duration{Duration: 5 * time.Second}
	private.Reply.MaxParts = maxParts
	if err := private.Validate(); err != nil {
		t.Fatalf("private policy: %v", err)
	}
	cfg.DefaultPrivatePolicy = &private
}

// typingTime is the pause before a later part of a reply: 800ms plus 60ms per
// character, between 1s and 4s.
func typingTime(part string) time.Duration {
	d := 800*time.Millisecond + 60*time.Millisecond*time.Duration(utf8.RuneCountInString(part))
	return min(max(d, time.Second), 4*time.Second)
}

func TestE2E_PrivateBurstGetsOneReply(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	e := newEnv(t, rhythmConfig(t, fake.URL, 600*time.Millisecond, 1))

	texts := []string{"今天好累", "加班到现在", "想吃点甜的"}
	var taskID string
	for i, text := range texts {
		if i > 0 {
			// Far longer than the group debounce, shorter than the private wait.
			time.Sleep(150 * time.Millisecond)
		}
		resp, _ := e.say(adminChat, admin, text, mock.SayOptions{})
		if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
			t.Fatalf("message %d: response %+v, want a reply task", i+1, resp)
		}
		if i == 0 {
			taskID = resp.TaskID
		} else if !resp.Merged || resp.TaskID != taskID {
			t.Fatalf("message %d: response %+v, want merged into %s", i+1, resp, taskID)
		}
	}
	a := e.drainOne()
	if a.Content.Text != fakeReply || a.PartIndex != 0 || a.PartCount != 0 {
		t.Errorf("reply = %+v, want %q as a single message", a, fakeReply)
	}
	e.requireNoAction(500 * time.Millisecond)
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(reqs))
	}
	prompt := requestText(reqs[0])
	for _, text := range texts {
		if !strings.Contains(prompt, text) {
			t.Errorf("model request lacks %q", text)
		}
	}
}

// partSlack is how late after it is due a reply part may be handed out.
const partSlack = time.Second

func TestE2E_PrivateReplyPartsArePaced(t *testing.T) {
	// Parts 2 and 3 take over 2s to type, so each is still pending right after
	// the part before it arrives; the core wakes the long poll once it is due,
	// well before the outbox's 2s recheck.
	parts := []string{
		"我刚到家",
		"今天路上特别堵，公交车走走停停开了快一个小时",
		"早点休息吧，明天还要早起上班，记得把闹钟定好，别又像上周那样睡过头了，晚安",
	}
	fake := fakeopenai.New(t, answer(strings.Join(parts, "\n|||\n")))
	e := newEnv(t, rhythmConfig(t, fake.URL, 50*time.Millisecond, 3))
	ctx := context.Background()

	e.say(adminChat, admin, "到家了吗", mock.SayOptions{})
	first := e.drainOne()
	due := first.CreatedAt
	got := []conloomapi.Action{first}
	for i := 1; i < len(parts); i++ {
		due = due.Add(typingTime(parts[i]))
		early, err := e.client.PollOutbox(ctx, 0, 0)
		if err != nil {
			t.Fatalf("PollOutbox: %v", err)
		}
		if len(early) != 0 {
			t.Fatalf("part %d handed out at %v right after part %d, want it held until %v: %+v", i+1, time.Now(), i, due, early)
		}
		a := e.drainOne()
		if at := time.Now(); at.Before(due) || at.After(due.Add(partSlack)) {
			t.Fatalf("part %d handed out at %v, want it within %v after it was due at %v", i+1, at, partSlack, due)
		}
		got = append(got, a)
	}
	for i, a := range got {
		if a.Content.Text != parts[i] || a.PartIndex != i+1 || a.PartCount != len(parts) {
			t.Errorf("action %d = %q (part %d of %d), want %q (part %d of %d)",
				i+1, a.Content.Text, a.PartIndex, a.PartCount, parts[i], i+1, len(parts))
		}
	}
	e.requireNoAction(300 * time.Millisecond)
	if n := fake.Calls(); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
}

func TestE2E_PrivateFollowUpDuringGenerationRestarts(t *testing.T) {
	const followUp = "要关于猫的"
	release := make(chan struct{})
	var calls atomic.Int32
	fake := fakeopenai.New(t, func(req map[string]any) (int, string) {
		if calls.Add(1) == 1 {
			<-release
			return 200, "从前有一座山"
		}
		if strings.Contains(requestText(req), followUp) {
			return 200, "好，讲一个关于猫的故事"
		}
		return 200, "从前有一座山"
	})
	var once atomic.Bool
	open := func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(open)
	e := newEnv(t, rhythmConfig(t, fake.URL, 50*time.Millisecond, 1))
	ctx := context.Background()

	first, _ := e.say(adminChat, admin, "讲个故事吧", mock.SayOptions{})
	if first.Disposition != conloomapi.DispositionReply || first.TaskID == "" {
		t.Fatalf("first message: response %+v, want a reply task", first)
	}
	waitUntil(t, "the model call to start", func() bool { return fake.Calls() == 1 })

	second, _ := e.say(adminChat, admin, followUp, mock.SayOptions{})
	if second.Disposition != conloomapi.DispositionReply || second.TaskID == "" || second.TaskID == first.TaskID {
		t.Fatalf("follow-up: response %+v, want a new reply task replacing %s", second, first.TaskID)
	}
	a := e.drainOne()
	if a.Content.Text != "好，讲一个关于猫的故事" {
		t.Fatalf("reply = %q, want the answer to both messages", a.Content.Text)
	}
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(reqs))
	}
	if prompt := requestText(reqs[1]); !strings.Contains(prompt, "讲个故事吧") || !strings.Contains(prompt, followUp) {
		t.Errorf("restarted request lacks one of the messages: %q", prompt)
	}

	// The abandoned generation finishing late sends nothing.
	open()
	e.requireNoAction(700 * time.Millisecond)
	task, err := e.client.GetTask(ctx, first.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != conloomapi.TaskCancelled || len(task.Actions) != 0 {
		t.Errorf("replaced task = %s with %d actions, want cancelled without actions", task.Status, len(task.Actions))
	}
	if n := fake.Calls(); n != 2 {
		t.Errorf("model calls = %d, want 2", n)
	}
}

func TestE2E_PanelEditsPrivateRhythm(t *testing.T) {
	fake := fakeopenai.New(t, answer(fakeReply))
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	cfg := panelTestConfig(t, configParams{modelURL: fake.URL}, hash)
	setPrivateRhythm(t, cfg, 50*time.Millisecond, 1)
	e := newEnv(t, cfg)
	e.say(adminChat, admin, "你好", mock.SayOptions{})
	e.drainOne()

	p := panelLogin(t, e.srv.URL)
	path := "/panel/api/conversations/" + url.PathEscape("bot_main:qq:private:private_29999") + "/policy"
	var pol struct {
		Current struct {
			Policy conloomapi.GroupPolicy `json:"policy"`
		} `json:"current"`
	}
	p.get(path, &pol)
	gp := pol.Current.Policy

	gp.Reply.MaxParts = 3
	gp.Reply.PrivateWait = conloomapi.Duration{Duration: 2 * time.Second}
	gp.Reply.PrivateMaxWait = conloomapi.Duration{Duration: time.Second}
	status, raw := p.do(http.MethodPut, path, map[string]any{"policy": gp, "reason": "等待时间写反了"})
	var refused conloomapi.ErrorResponse
	if err := json.Unmarshal(raw, &refused); err != nil || status != http.StatusBadRequest ||
		!strings.Contains(refused.Error.Message, "reply.private_max_wait must be in [reply.private_wait (2s), 2m], got 1s") {
		t.Fatalf("PUT with private_max_wait below private_wait: status %d, body %s; want 400 naming the field", status, raw)
	}

	gp.Reply.PrivateWait = conloomapi.Duration{}
	gp.Reply.PrivateMaxWait = conloomapi.Duration{}
	if status, raw := p.do(http.MethodPut, path, map[string]any{"policy": gp, "reason": "分条，等待用默认值"}); status != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", status, raw)
	}
	p.get(path, &pol)
	got := pol.Current.Policy.Reply
	if got.MaxParts != 3 || got.PrivateWait != cfg.DefaultPrivatePolicy.Reply.PrivateWait ||
		got.PrivateMaxWait != cfg.DefaultPrivatePolicy.Reply.PrivateMaxWait {
		t.Fatalf("stored reply = max_parts %d, private_wait %s, private_max_wait %s; want 3 and the private defaults %s, %s",
			got.MaxParts, got.PrivateWait, got.PrivateMaxWait,
			cfg.DefaultPrivatePolicy.Reply.PrivateWait, cfg.DefaultPrivatePolicy.Reply.PrivateMaxWait)
	}
}
