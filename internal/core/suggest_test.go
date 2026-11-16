package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// suggestJSON is a model answer of three suggestions, suggestWant.
const suggestJSON = "```json\n[\"火锅好吃吗\", \"下次一起去吧\", \"最近在看什么书\"]\n```"

var suggestWant = []string{"火锅好吃吗", "下次一起去吧", "最近在看什么书"}

// panelLineBot marks a line of seedPanelChat and addPanelLines as the bot's.
const panelLineBot = "bot:"

// addPanelLinesAt stores lines in the panel chat key, oldest first, the first
// at at and each next one a millisecond later: a line starting with
// panelLineBot is the bot's, the others are the panel user 小明's. It returns
// the stored messages, with their seq.
func (e *testEnv) addPanelLinesAt(t *testing.T, key string, at time.Time, lines ...string) []store.MessageRecord {
	t.Helper()
	ctx := context.Background()
	out := make([]store.MessageRecord, 0, len(lines))
	for i, line := range lines {
		n := e.seq.Add(1)
		when := at.Add(time.Duration(i) * time.Millisecond).UTC()
		m := store.MessageRecord{
			ID: fmt.Sprintf("msg_seed_%d", n), BotInstanceID: testBot, ConversationKey: key, Platform: store.PlatformPanel,
			PlatformMessageID: fmt.Sprintf("panel_seed_%d", n), Channel: store.ChannelPanel, OccurredAt: when, CreatedAt: when,
		}
		if text, ok := strings.CutPrefix(line, panelLineBot); ok {
			m.Text, m.FromBot = text, true
		} else {
			m.Text, m.ActorID, m.PlatformUserID, m.DisplayName = line, "panel_admin", "panel_admin", "小明"
		}
		_, err := e.st.Ingest(ctx, store.Ingest{
			Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed:" + m.ID, EventID: "evt_" + m.ID,
				Response: []byte("{}"), CreatedAt: when},
			Message: &m,
		})
		if err != nil {
			t.Fatal(err)
		}
		stored, err := e.st.GetMessage(ctx, testBot, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, stored)
	}
	return out
}

func (e *testEnv) addPanelLines(t *testing.T, key string, lines ...string) []store.MessageRecord {
	t.Helper()
	return e.addPanelLinesAt(t, key, time.Now(), lines...)
}

// seedPanelChat creates a panel chat of testBot with lines (see
// addPanelLinesAt) and returns its key and the seq of its newest message.
func (e *testEnv) seedPanelChat(t *testing.T, lines ...string) (string, int64) {
	t.Helper()
	key := e.createChat(t, testBot)
	msgs := e.addPanelLines(t, key, lines...)
	return key, msgs[len(msgs)-1].Seq
}

func (e *testEnv) suggest(t *testing.T, key string, seq int64) []string {
	t.Helper()
	got, err := e.o.Suggest(context.Background(), key, seq)
	if err != nil {
		t.Fatalf("Suggest(%s, %d): %v", key, seq, err)
	}
	if got == nil {
		t.Fatalf("Suggest(%s, %d) = nil, want a list", key, seq)
	}
	return got
}

// isSuggestRequest reports whether req asks the model for reply suggestions.
func isSuggestRequest(req map[string]any) bool {
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		return false
	}
	m, _ := msgs[0].(map[string]any)
	sys, _ := m["content"].(string)
	return strings.HasPrefix(sys, strings.SplitN(suggestPrompt, "“", 2)[0])
}

func TestSuggestions_CachedPerSeq(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: answer(suggestJSON)})
	written := recordUsage(t, env)
	key, first := env.seedPanelChat(t, "今天吃了火锅", panelLineBot+"听起来很香")

	for i := range 2 {
		if got := env.suggest(t, key, first); !slices.Equal(got, suggestWant) {
			t.Fatalf("request %d = %q, want %q", i+1, got, suggestWant)
		}
	}
	if n := env.short.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1: the same message's suggestions come from the cache", n)
	}

	more := env.addPanelLines(t, key, "是啊", panelLineBot+"下次带上我")
	next := more[len(more)-1].Seq
	if got := env.suggest(t, key, next); !slices.Equal(got, suggestWant) {
		t.Fatalf("newer message = %q, want %q", got, suggestWant)
	}
	if n := env.short.Calls(); n != 2 {
		t.Fatalf("model calls = %d, want 2: a newer message gets its own", n)
	}
	if got := env.suggest(t, key, first); len(got) != 0 {
		t.Errorf("older message = %q, want none: it is no longer the newest", got)
	}
	if got := env.suggest(t, key, next); !slices.Equal(got, suggestWant) || env.short.Calls() != 2 {
		t.Errorf("newer message again = %q after %d calls, want it cached", got, env.short.Calls())
	}

	calls := written()
	if len(calls) != 2 {
		t.Fatalf("recorded %d calls, want 2: %+v", len(calls), calls)
	}
	for _, c := range calls {
		if c.Purpose != store.PurposeSuggest || c.Role != string(router.RoleShort) || c.Model != "short-model" ||
			c.BotInstanceID != testBot || c.ConversationKey != key || c.TaskID != "" || c.Outcome != store.OutcomeOK ||
			c.EstimatedTokens <= 0 {
			t.Errorf("call = %+v", c)
		}
	}
	logs := env.logs.String()
	if strings.Count(logs, `msg="reply suggestions generated"`) != 2 {
		t.Errorf("logs lack two generation records:\n%s", logs)
	}
	for _, text := range append([]string{"今天吃了火锅", "下次带上我"}, suggestWant...) {
		if strings.Contains(logs, text) {
			t.Errorf("logs carry the text %q:\n%s", text, logs)
		}
	}
}

func TestSuggestions_CacheKeepsRecentChats(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: answer(suggestJSON)})
	if env.o.suggestions.limit != 256 {
		t.Errorf("cache limit = %d, want 256 chats", env.o.suggestions.limit)
	}
	env.o.suggestions.limit = 2
	a, seqA := env.seedPanelChat(t, panelLineBot+"甲")
	b, seqB := env.seedPanelChat(t, panelLineBot+"乙")
	c, seqC := env.seedPanelChat(t, panelLineBot+"丙")

	for _, step := range []struct {
		key   string
		seq   int64
		calls int
	}{
		{a, seqA, 1}, {b, seqB, 2}, {a, seqA, 2}, {c, seqC, 3}, // c evicts b, the least recently asked for
		{a, seqA, 3}, {b, seqB, 4},
	} {
		env.suggest(t, step.key, step.seq)
		if n := env.short.Calls(); n != step.calls {
			t.Fatalf("after %s: model calls = %d, want %d", step.key, n, step.calls)
		}
	}
}

func TestSuggestions_SingleFlight(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) {
		<-release
		return 200, suggestJSON
	}})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	key, first := env.seedPanelChat(t, "在吗", panelLineBot+"在的")

	type result struct {
		seq   int64
		items []string
	}
	results := make(chan result, 4)
	ask := func(seq int64) {
		go func() {
			got, err := env.o.Suggest(context.Background(), key, seq)
			if err != nil {
				t.Errorf("Suggest(%d): %v", seq, err)
			}
			results <- result{seq, got}
		}()
	}
	for range 3 {
		ask(first)
	}
	waitUntil(t, "the first request to reach the model", func() bool { return env.short.Calls() == 1 })
	// A request for the next message waits for the generation in progress.
	more := env.addPanelLines(t, key, "吃了吗", panelLineBot+"吃过了")
	next := more[len(more)-1].Seq
	ask(next)
	time.Sleep(100 * time.Millisecond)
	if n := env.short.Calls(); n != 1 {
		t.Fatalf("model calls while one is generating = %d, want 1", n)
	}
	once.Do(func() { close(release) })
	for range 4 {
		select {
		case r := <-results:
			if !slices.Equal(r.items, suggestWant) {
				t.Errorf("Suggest(%d) = %q, want %q", r.seq, r.items, suggestWant)
			}
		case <-time.After(waitLimit):
			t.Fatal("timed out waiting for the suggestions")
		}
	}
	if n := env.short.Calls(); n != 2 {
		t.Fatalf("model calls = %d, want 2: one shared by the first message's requests, one for the next", n)
	}
}

func TestSuggestions_ProtectedReviewed(t *testing.T) {
	word := protectedWord(t)
	answerJSON, err := json.Marshal([]string{"想你了，" + word, "今天吃什么", "讲个故事吧", "周末去哪玩"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		adult bool
		want  []string
		rules bool
	}{
		{"protected", false, []string{"今天吃什么", "讲个故事吧", "周末去哪玩"}, true},
		{"open", true, []string{"想你了，" + word, "今天吃什么", "讲个故事吧"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{short: answer(string(answerJSON)), configure: withPersona(false, c.adult)})
			key, seq := env.seedPanelChat(t, "在吗", panelLineBot+"在的")
			if got := env.suggest(t, key, seq); !slices.Equal(got, c.want) {
				t.Fatalf("suggestions = %q, want %q", got, c.want)
			}
			sys, _ := requestMessages(t, env.short.Requests()[0])[0]["content"].(string)
			if got := strings.Contains(sys, persona.ProtectionRules); got != c.rules {
				t.Errorf("prompt has the protection rules = %v, want %v:\n%s", got, c.rules, sys)
			}
		})
	}
}

func TestSuggestions_ThinkingDisabled(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{
		short: func(req map[string]any) (int, string) {
			if isSuggestRequest(req) {
				return 200, suggestJSON
			}
			return 200, fakeReply
		},
		configure: func(c *config.Config) {
			p := c.ModelProfiles[string(router.RoleShort)]
			p.ExtraBody = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true, "keep": "x"}, "top_k": 20}
			c.ModelProfiles[string(router.RoleShort)] = p
		},
	})
	key, seq := env.seedPanelChat(t, "在吗", panelLineBot+"在的")
	if got := env.suggest(t, key, seq); !slices.Equal(got, suggestWant) {
		t.Fatalf("suggestions = %q", got)
	}
	req := env.short.Requests()[0]
	if !isSuggestRequest(req) {
		t.Fatalf("first request is not the suggestions': %v", req)
	}
	want := map[string]any{"enable_thinking": false, "keep": "x"}
	if got := req["chat_template_kwargs"]; !reflect.DeepEqual(got, want) || req["top_k"] != float64(20) {
		t.Errorf("chat_template_kwargs = %v, top_k = %v; want %v and the profile's 20", got, req["top_k"], want)
	}

	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "在吗", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("reply task = %+v", task)
	}
	reqs := env.short.Requests()
	want = map[string]any{"enable_thinking": true, "keep": "x"}
	if got := reqs[len(reqs)-1]["chat_template_kwargs"]; !reflect.DeepEqual(got, want) {
		t.Errorf("reply chat_template_kwargs = %v, want the profile's %v", got, want)
	}
}

func TestThinkingOff(t *testing.T) {
	for _, c := range []struct {
		name  string
		extra map[string]any
		want  map[string]any
	}{
		{"no extra_body", nil, nil},
		{"other keys", map[string]any{"top_k": 20}, nil},
		{"chat_template_kwargs", map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true}},
			map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}},
		{"top-level enable_thinking", map[string]any{"enable_thinking": true}, map[string]any{"enable_thinking": false}},
		{"both", map[string]any{"enable_thinking": true, "chat_template_kwargs": map[string]any{}},
			map[string]any{"enable_thinking": false, "chat_template_kwargs": map[string]any{"enable_thinking": false}}},
		{"chat_template_kwargs not an object", map[string]any{"chat_template_kwargs": "x"}, nil},
	} {
		if got := thinkingOff(c.extra); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: thinkingOff = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSuggestions_BadModelOutputEmpty(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  int
		content string
		outcome string
	}{
		{"not JSON", 200, "我想不出来", store.OutcomeOK},
		{"not strings", 200, "[1, 2, null]", store.OutcomeOK},
		{"too long", 200, `["` + strings.Repeat("长", 41) + `"]`, store.OutcomeOK},
		{"blank", 200, `["", "  "]`, store.OutcomeOK},
		{"model error", 500, "upstream down", store.OutcomeError},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) { return c.status, c.content }})
			written := recordUsage(t, env)
			key, seq := env.seedPanelChat(t, "在吗", panelLineBot+"在的")
			for i := range 2 {
				if got := env.suggest(t, key, seq); len(got) != 0 {
					t.Fatalf("request %d = %q, want none", i+1, got)
				}
			}
			if n := env.short.Calls(); n != 2 {
				t.Errorf("model calls = %d, want 2: nothing usable is not cached", n)
			}
			for _, call := range written() {
				if call.Purpose != store.PurposeSuggest || call.Outcome != c.outcome {
					t.Errorf("call = %+v, want purpose suggest, outcome %s", call, c.outcome)
				}
			}
		})
	}
}

func TestParseSuggestions(t *testing.T) {
	forty := strings.Repeat("四", 40)
	for _, c := range []struct {
		name, in string
		want     []string
		err      bool
	}{
		{"plain", `["一","二","三"]`, []string{"一", "二", "三"}, false},
		{"fenced, trimmed, other items and repeats left out", "```json\n[\" 你好呀 \", 3, \"\", \"你好呀\", \"再说一次\"]\n```",
			[]string{"你好呀", "再说一次"}, false},
		{"text around, all kept", "好的：\n[\"一\",\"二\",\"三\",\"四\"]\n希望有用 [1]", []string{"一", "二", "三", "四"}, false},
		{"a bracket before the array", "[笑] 给你：[\"一\"]", []string{"一"}, false},
		{"40 characters kept, 41 not", `["` + forty + `","` + forty + `多"]`, []string{forty}, false},
		{"empty array", "[]", []string{}, false},
		{"no array", "我想不出来", nil, true},
		{"unterminated", `["一", "二"`, nil, true},
	} {
		got, err := parseSuggestions(c.in)
		if (err != nil) != c.err || !slices.Equal(got, c.want) || (err == nil && got == nil) {
			t.Errorf("%s: parseSuggestions = %q, %v; want %q, error %v", c.name, got, err, c.want, c.err)
		}
	}
}

func TestSuggestions_PanelOnly(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: allowAllPrivate})
	ctx := context.Background()
	env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗")).TaskID)
	env.waitTask(t, env.submit(t, env.groupEvent("20001", "在吗", mentionBot)).TaskID)
	calls := env.short.Calls()

	for _, c := range []struct {
		name, key string
		want      error
	}{
		{"platform private chat", privateKey("20001"), ErrNotPanelChat},
		{"platform group", testConvKey, ErrNotPanelChat},
		{"unknown panel chat", store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_missing"), store.ErrNotFound},
		{"malformed key", "bot_main:panel", store.ErrNotFound},
	} {
		if got, err := env.o.Suggest(ctx, c.key, 1); !errors.Is(err, c.want) || got != nil {
			t.Errorf("%s: Suggest = %q, %v; want %v", c.name, got, err, c.want)
		}
	}

	key := env.createChat(t, testBot)
	msgs := env.addPanelLines(t, key, panelLineBot+"你好", "在吗")
	for _, c := range []struct {
		name string
		seq  int64
	}{
		{"the newest message is the user's", msgs[1].Seq},
		{"not the newest message", msgs[0].Seq},
		{"no such message", msgs[1].Seq + 100},
	} {
		if got := env.suggest(t, key, c.seq); len(got) != 0 {
			t.Errorf("%s: suggestions = %q, want none", c.name, got)
		}
	}
	if n := env.short.Calls(); n != calls {
		t.Errorf("model calls = %d, want %d: none for suggestions", n, calls)
	}
}

func TestSuggestions_Prompt(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: answer(suggestJSON), configure: func(c *config.Config) {
		doc := conloomapi.DefaultPersona()
		doc.PersonaName, doc.CharacterName = "灯塔看守", "苔灯"
		c.LegacyPersonaTemplates["default"] = doc
	}})
	key := env.createChat(t, testBot)
	long := strings.Repeat("长", 250)
	var lines []string
	for i := range 14 {
		if i%2 == 0 {
			lines = append(lines, fmt.Sprintf("第%d句\n换行", i))
		} else {
			lines = append(lines, fmt.Sprintf("%s回%d", panelLineBot, i))
		}
	}
	lines = append(lines, long, panelLineBot+"好的")
	msgs := env.addPanelLines(t, key, lines...)
	env.suggest(t, key, msgs[len(msgs)-1].Seq)

	req := env.short.Requests()[0]
	if req["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v, want the reserved output 256", req["max_tokens"])
	}
	parts := requestMessages(t, req)
	if len(parts) != 2 || parts[0]["role"] != "system" || parts[1]["role"] != "user" {
		t.Fatalf("messages = %v, want a system and a user message", parts)
	}
	sys, _ := parts[0]["content"].(string)
	for _, want := range []string{"“苔灯”", "3 句", "40 个字", "追问", "剧情", "新话题", "JSON 数组"} {
		if !strings.Contains(sys, want) {
			t.Errorf("prompt lacks %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "{bot_name}") || strings.Contains(sys, "织语") {
		t.Errorf("prompt does not call the bot by its character name:\n%s", sys)
	}
	var want []string
	for i := 4; i < 14; i++ {
		if i%2 == 0 {
			want = append(want, fmt.Sprintf("小明：第%d句 换行", i))
		} else {
			want = append(want, fmt.Sprintf("苔灯：回%d", i))
		}
	}
	want = append(want, "小明："+strings.Repeat("长", 200)+"…", "苔灯：好的")
	if got, _ := parts[1]["content"].(string); got != strings.Join(want, "\n") {
		t.Errorf("transcript =\n%s\nwant the newest 12 messages, one line each, long ones cut:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestSuggestions_SinceForgotten(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: answer(suggestJSON)})
	key := env.createChat(t, testBot)
	now := time.Now()
	env.addPanelLinesAt(t, key, now.Add(-time.Hour), "我叫小明，住在海边", panelLineBot+"记住啦")
	if err := env.st.ForgetPrivateScope(context.Background(), memory.ScopeKeyOf(key), []string{key}, "panel_admin",
		now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	msgs := env.addPanelLinesAt(t, key, now, "你好", panelLineBot+"你好呀")
	env.suggest(t, key, msgs[1].Seq)
	got, _ := requestMessages(t, env.short.Requests()[0])[1]["content"].(string)
	if want := "小明：你好\n织语：你好呀"; got != want {
		t.Errorf("transcript = %q, want only the messages after the forget %q", got, want)
	}
}

func TestSuggestions_Timeout(t *testing.T) {
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) {
		<-release
		return 200, suggestJSON
	}})
	t.Cleanup(func() { close(release) })
	if env.o.suggestTimeout != 20*time.Second {
		t.Errorf("suggest timeout = %v, want 20s", env.o.suggestTimeout)
	}
	written := recordUsage(t, env)
	env.o.suggestTimeout = 100 * time.Millisecond
	key, seq := env.seedPanelChat(t, "在吗", panelLineBot+"在的")

	start := time.Now()
	if got := env.suggest(t, key, seq); len(got) != 0 {
		t.Fatalf("suggestions = %q, want none", got)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v, want the timeout to stop it", d)
	}
	if calls := written(); len(calls) != 1 || calls[0].Outcome != store.OutcomeTimeout || calls[0].Purpose != store.PurposeSuggest {
		t.Errorf("calls = %+v, want one timed-out suggest call", calls)
	}
	if s := env.o.adm.Stats(string(router.RoleShort)); s.InFlight != 0 {
		t.Errorf("admission stats after the timeout = %+v, want the lease released", s)
	}
}
