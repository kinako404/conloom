package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// offered returns the names of the tools req offers.
func offered(req map[string]any) []string {
	var names []string
	tools, _ := req["tools"].([]any)
	for _, tool := range tools {
		fn, _ := tool.(map[string]any)["function"].(map[string]any)
		name, _ := fn["name"].(string)
		names = append(names, name)
	}
	return names
}

func enableThinking(req map[string]any) any {
	kw, _ := req["chat_template_kwargs"].(map[string]any)
	return kw["enable_thinking"]
}

func hasToolMessage(req map[string]any) bool {
	msgs, _ := req["messages"].([]any)
	return slices.ContainsFunc(msgs, func(m any) bool { return m.(map[string]any)["role"] == "tool" })
}

func isStateRequest(req map[string]any) bool {
	return strings.Contains(lastUserMessage(req), "【状态更新】")
}

// TestE2E_StateStepAfterReplyWithBuiltinTools runs a private turn in the
// default after_reply mode: the reply offers no state tool and turns thinking
// off; the state step afterwards calls the real set_status, adjust_buff and
// remember, whose changes are stored, and sends nothing.
func TestE2E_StateStepAfterReplyWithBuiltinTools(t *testing.T) {
	awayFromMidnight(t)
	const question = "记住我喜欢草莓。今天好开心，我先去洗澡了"
	var stateDone atomic.Bool
	fake := fakeopenai.NewWithTools(t, func(req map[string]any) fakeopenai.Reply {
		switch {
		case !isStateRequest(req):
			return fakeopenai.Reply{Content: "好，我去洗澡啦。"}
		case hasToolMessage(req):
			stateDone.Store(true)
			return fakeopenai.Reply{Content: "随便写点"}
		}
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{
			{ID: "s1", Name: "set_status", Arguments: `{"activity":"洗澡","minutes":20}`},
			{ID: "s2", Name: "adjust_buff", Arguments: `{"name":"心情","delta":3,"reason":"对方很开心"}`},
			{ID: "s3", Name: "remember", Arguments: `{"content":"我喜欢草莓"}`},
		}}
	})
	cfg := memoryTestConfig(t, fake.URL)
	for name, p := range cfg.ModelProfiles {
		p.ExtraBody = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true}}
		cfg.ModelProfiles[name] = p
	}
	private := conloomapi.DefaultGroupPolicy()
	private.Memory.Enabled = true
	private.Memory.ConsolidateMin = 1000
	private.Reply.PrivateWait = conloomapi.Duration{Duration: 100 * time.Millisecond}
	cfg.DefaultPrivatePolicy = &private
	seedBotSettings(t, cfg, func(s *store.BotSettings) { s.Skills = []string{"memory", "status", "buff"} })
	e := newEnv(t, cfg)

	resp, _ := e.say(mock.Conv{Type: conloomapi.ConvPrivate, PlatformID: admin.PlatformID}, admin, question, mock.SayOptions{})
	if resp.Disposition != conloomapi.DispositionReply {
		t.Fatalf("response = %+v, want a reply task", resp)
	}
	if got := e.drainOne().Content.Text; got != "好，我去洗澡啦。" {
		t.Fatalf("reply = %q", got)
	}

	ctx := context.Background()
	admins := client.New(e.srv.URL, memAdminToken, nil)
	var status store.ConversationStatus
	var mood *store.BuffValue
	var note string
	waitUntil(t, "the state step's changes", func() bool {
		if !stateDone.Load() {
			return false
		}
		st, err := admins.GetMemory(ctx, "private_29999")
		if err != nil {
			t.Fatal(err)
		}
		m, _ := memberOf(st, admin.PlatformID)
		note = m.Note
		withDB(t, cfg, func(db *sqlite.DB) {
			var err error
			if status, err = db.GetStatus(ctx, adminPrivateKey); err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
			buffs, err := db.GetBuffs(ctx, adminPrivateKey)
			if err != nil {
				t.Fatal(err)
			}
			mood = nil
			for i := range buffs {
				if buffs[i].Name == "心情" {
					mood = &buffs[i]
				}
			}
		})
		return status.Activity != "" && mood != nil && note != ""
	})
	if status.Activity != "洗澡" || !status.UntilAt.After(time.Now()) {
		t.Errorf("status = %+v", status)
	}
	if mood.DayDelta != 3 {
		t.Errorf("心情 = %+v, want raised by 3", mood)
	}
	if !strings.Contains(note, memory.RememberMarker+"我喜欢草莓") {
		t.Errorf("note = %q", note)
	}

	var replies, states []map[string]any
	for _, req := range fake.Requests() {
		if isStateRequest(req) {
			states = append(states, req)
		} else if strings.Contains(lastUserMessage(req), question) {
			replies = append(replies, req)
		}
	}
	stateTools := []string{"remember", "set_status", "set_scene", "adjust_buff", "add_buff", "remove_buff"}
	if len(replies) != 1 {
		t.Fatalf("reply requests = %d, want 1", len(replies))
	}
	for _, name := range offered(replies[0]) {
		if slices.Contains(stateTools, name) {
			t.Errorf("reply request offers the state tool %s", name)
		}
	}
	if got := enableThinking(replies[0]); got != false {
		t.Errorf("reply enable_thinking = %v, want false", got)
	}
	if len(states) != 2 {
		t.Fatalf("state requests = %d, want 2", len(states))
	}
	for _, req := range states {
		for _, name := range offered(req) {
			if !slices.Contains(stateTools, name) {
				t.Errorf("state request offers the lookup %s", name)
			}
		}
		if got := enableThinking(req); got != true {
			t.Errorf("state enable_thinking = %v, want true", got)
		}
		if last := lastUserMessage(req); !strings.Contains(last, question) || !strings.HasSuffix(last, "「好，我去洗澡啦。」") {
			t.Errorf("state request's last user message = %q", last)
		}
	}
	e.requireNoAction(500 * time.Millisecond)
}
