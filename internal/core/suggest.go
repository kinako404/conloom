package core

import (
	"cmp"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// suggestCount is how many reply suggestions a chat is offered, and
	// suggestMaxRunes the most characters of one.
	suggestCount    = 3
	suggestMaxRunes = 40
	// suggestHistory is how many of a chat's newest messages the suggestions
	// are written from, and suggestLineRunes the most characters of one of
	// them the model sees.
	suggestHistory   = 12
	suggestLineRunes = 200
	// suggestTimeout bounds generating a chat's suggestions.
	suggestTimeout = 20 * time.Second
	// suggestCacheSize is how many chats' suggestions are kept.
	suggestCacheSize = 256
)

// suggestPrompt asks the model for the user's next lines to the bot
// {bot_name}; the chat's recent messages follow as the user message.
const suggestPrompt = `你在帮一位用户想下一句话。用户正在和“{bot_name}”聊天，下一条消息是他们最近的聊天记录，每行开头是说话人的名字，“{bot_name}：”开头的是{bot_name}说的，其余都是用户说的。

请替用户写 3 句接下来可以发给{bot_name}的话：
- 用用户自己的口吻，直接写要说的话；不要写{bot_name}的回复，开头也不要带名字。
- 每句不超过 40 个字。
- 3 句要有区别：一句接着刚才的话追问，一句推动剧情或事情往下发展，一句换一个新话题。
- 贴合聊天的语气和内容，不要重复已经说过的话。
- 只输出一个 JSON 数组，里面是 3 个字符串，例如 ["……","……","……"]，不要输出别的内容。`

// errNoSuggestions reports a model answer without a JSON array.
var errNoSuggestions = errors.New("core: no JSON array in the suggestions")

// Suggest returns up to three lines the user of the panel chat convKey might
// send next, in their own voice, once the bot's turn is done: afterSeq is the
// seq of the chat's newest message, which must be the bot's. They are
// generated once per message and kept for the suggestCacheSize chats most
// recently asked for; while a chat's suggestions are being generated, other
// requests in that chat wait for them, and share them when they are for the
// same message. The list is empty, never nil, when the message is not the
// chat's newest or not the bot's, or generating fails (logged); see suggest.
// A conversation on another platform is ErrNotPanelChat, an unknown one
// store.ErrNotFound, and a request whose ctx ends while it waits ctx's error.
func (o *Orchestrator) Suggest(ctx context.Context, convKey string, afterSeq int64) ([]string, error) {
	conv, err := o.panelConversation(ctx, convKey)
	if err != nil {
		return nil, err
	}
	if conv.Platform != store.PlatformPanel {
		return nil, ErrNotPanelChat
	}
	for {
		items, cached, f, lead := o.suggestions.join(convKey, afterSeq)
		if cached {
			return items, nil
		}
		if lead {
			return o.leadSuggestions(conv, f), nil
		}
		select {
		case <-f.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if f.seq == afterSeq {
			return slices.Clone(f.items), nil
		}
	}
}

// leadSuggestions generates the suggestions of the flight f in conv and ends
// f with them, with none if generating panics.
func (o *Orchestrator) leadSuggestions(conv store.ConversationRecord, f *suggestFlight) []string {
	items := []string{}
	defer func() { o.suggestions.finish(conv.Key, f, items) }()
	items = o.suggest(conv, f.seq)
	return slices.Clone(items)
}

// suggest generates the reply suggestions of the panel chat conv after its
// message seq, with the short_context model, its thinking turned off (see
// thinkingOff): the model sees the bot's character name and the chat's
// newest suggestHistory messages, since its user last asked to be forgotten
// (see sinceUserForgot), and in a protected chat the same protection rules as
// replies. Lines the content mode's output review does not let through
// unchanged are dropped, and at most suggestCount kept. It returns none when
// seq is not the chat's newest message or that is not the bot's, there is no
// short_context profile or its breaker is open, or anything fails (logged).
// The work is not tied to a request, so suggestions whose requester left
// still serve the next one; it takes at most suggestTimeout and ends at
// Close. The model call is recorded with purpose suggest, failed or not; no
// text is logged.
func (o *Orchestrator) suggest(conv store.ConversationRecord, seq int64) []string {
	none := []string{}
	ctx, cancel := context.WithTimeout(o.bgCtx, o.suggestTimeout)
	defer cancel()
	p, ok := o.router.Profile(router.RoleShort)
	if !ok || o.router.BreakerOpen(router.RoleShort) {
		return none
	}
	recent, err := o.st.RecentMessages(ctx, conv.Key, suggestHistory)
	if err != nil {
		o.log.Warn("load messages for reply suggestions failed", "conversation", conv.Key, "error", err)
		return none
	}
	if n := len(recent); n == 0 || recent[n-1].Seq != seq || !recent[n-1].FromBot {
		return none
	}
	bot, ok := o.bots.Bot(conv.BotInstanceID)
	if !ok {
		return none
	}
	system, err := o.personas.SystemPrompt(ctx, conv.Key, bot.TemplateFor(conv.Type), bot.Name)
	if err != nil {
		o.log.Warn("load persona for reply suggestions failed", "conversation", conv.Key, "error", err)
		return none
	}
	pv, err := o.policies.Current(ctx, conv.Key)
	if err != nil {
		o.log.Warn("load policy for reply suggestions failed", "conversation", conv.Key, "error", err)
		return none
	}
	recent = o.sinceUserForgot(ctx, conv.Key, recent)
	prompt := strings.ReplaceAll(suggestPrompt, "{bot_name}", system.BotName)
	if system.Mode != conloomapi.ContentOpen {
		prompt += "\n\n" + persona.ProtectionRules
	}
	transcript := suggestTranscript(recent, system.BotName)

	role, limits := string(p.Role), p.Config.Context
	estimate := contextbuilder.Estimate(prompt+"\n"+transcript, p.Config.TokenEstimateFactor)
	lease, err := o.adm.Acquire(ctx, admission.Request{
		ID:       "suggest:" + conv.Key,
		Profile:  role,
		Tokens:   estimate + limits.ReservedOutputTokens + limits.SafetyMarginTokens,
		Priority: admission.PriorityBackground,
		TTL:      o.suggestTimeout + leaseSlack,
	})
	if err != nil {
		o.log.Warn("reply suggestions not admitted", "conversation", conv.Key, "role", role, "error", err)
		return none
	}
	defer lease.Release()

	req := openai.ChatRequest{
		Messages:  []openai.ChatMessage{{Role: "system", Content: prompt}, {Role: "user", Content: transcript}},
		MaxTokens: limits.ReservedOutputTokens,
		ExtraBody: thinkingOff(p.Config.ExtraBody),
	}
	start := time.Now()
	resp, err := p.Client.Chat(ctx, req)
	latency := time.Since(start)
	o.usage.Record(store.ModelCall{
		At: start.UTC(), BotInstanceID: conv.BotInstanceID, ConversationKey: conv.Key,
		Purpose: store.PurposeSuggest, Role: role, Model: cmp.Or(resp.Model, p.Config.Model),
		PromptTokens: resp.PromptTokens, CompletionTokens: resp.CompletionTokens, ReasoningTokens: resp.ReasoningTokens,
		EstimatedTokens: estimate, LatencyMS: latency.Milliseconds(), Outcome: usage.Outcome(err), FinishReason: resp.FinishReason,
	})
	if err != nil {
		o.log.Warn("reply suggestions failed", "conversation", conv.Key, "role", role,
			"latency_ms", latency.Milliseconds(), "error", err)
		return none
	}
	o.metrics.ModelLatency.WithLabelValues(role, resp.Model).Observe(latency.Seconds())
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "prompt").Add(float64(resp.PromptTokens))
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "completion").Add(float64(resp.CompletionTokens))
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "reasoning").Add(float64(resp.ReasoningTokens))
	candidates, err := parseSuggestions(resp.Content)
	if err != nil {
		o.log.Info("reply suggestions unusable", "conversation", conv.Key, "role", role, "model", resp.Model,
			"finish_reason", resp.FinishReason, "error", err)
		return none
	}
	check := o.reviewer.OutputProtected
	if system.Mode == conloomapi.ContentOpen {
		check = o.reviewer.Output
	}
	items := none
	for _, c := range candidates {
		if check(c, pv.Policy.Review).Verdict == review.VerdictAllow {
			items = append(items, c)
		}
		if len(items) == suggestCount {
			break
		}
	}
	o.log.Info("reply suggestions generated", "conversation", conv.Key, "role", role, "model", resp.Model,
		"latency_ms", latency.Milliseconds(), "prompt_tokens", resp.PromptTokens, "completion_tokens", resp.CompletionTokens,
		"reasoning_tokens", resp.ReasoningTokens, "finish_reason", resp.FinishReason,
		"candidates", len(candidates), "suggestions", len(items))
	return items
}

// sinceUserForgot drops the messages of recent from before the chat's user
// last asked to be forgotten (see sinceForgotten), the user being the sender
// of its newest message not from the bot. A failed read is logged and drops
// nothing.
func (o *Orchestrator) sinceUserForgot(ctx context.Context, convKey string, recent []store.MessageRecord) []store.MessageRecord {
	user := ""
	for _, m := range slices.Backward(recent) {
		if !m.FromBot {
			user = m.PlatformUserID
			break
		}
	}
	if user == "" {
		return recent
	}
	rows, err := o.st.GetMemberMemories(ctx, memory.ScopeKeyOf(convKey), []string{user})
	if err != nil {
		o.log.Warn("load member memory for reply suggestions failed", "conversation", convKey, "error", err)
		return recent
	}
	return sinceForgotten(recent, rows[user].ForgottenAt)
}

// suggestTranscript writes msgs, oldest first, one line each: the speaker's
// name (botName for the bot's messages, the sender's display name or 用户 for
// the others), a full-width colon and the text with its white space runs as
// single spaces, cut to suggestLineRunes characters. Messages without text are
// left out.
func suggestTranscript(msgs []store.MessageRecord, botName string) string {
	var lines []string
	for _, m := range msgs {
		text := strings.Join(strings.Fields(m.Text), " ")
		if text == "" {
			continue
		}
		if r := []rune(text); len(r) > suggestLineRunes {
			text = string(r[:suggestLineRunes]) + "…"
		}
		name := botName
		if !m.FromBot {
			name = cmp.Or(strings.TrimSpace(m.DisplayName), "用户")
		}
		lines = append(lines, name+"："+text)
	}
	return strings.Join(lines, "\n")
}

// parseSuggestions reads the candidate lines from a model's answer: the first
// JSON array in it, which may be wrapped in a code fence or other text. Its
// strings, trimmed, are the candidates, in order; other items, blank strings,
// strings over suggestMaxRunes characters and repeats are left out. An answer
// without a JSON array is errNoSuggestions.
func parseSuggestions(content string) ([]string, error) {
	for rest := content; ; rest = rest[1:] {
		i := strings.IndexByte(rest, '[')
		if i < 0 {
			return nil, errNoSuggestions
		}
		rest = rest[i:]
		var raw []json.RawMessage
		if json.NewDecoder(strings.NewReader(rest)).Decode(&raw) != nil {
			continue
		}
		out := []string{}
		for _, r := range raw {
			var s string
			if json.Unmarshal(r, &s) != nil {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" || utf8.RuneCountInString(s) > suggestMaxRunes || slices.Contains(out, s) {
				continue
			}
			out = append(out, s)
		}
		return out, nil
	}
}

// thinkingOff is the extra_body that turns thinking off for one request to a
// profile whose extra_body is extra: enable_thinking false in its
// chat_template_kwargs object and at its top level, each only where extra
// sets that, so no field the model service may not know is sent. It is nil
// when extra sets neither.
func thinkingOff(extra map[string]any) map[string]any {
	var out map[string]any
	if _, ok := extra["chat_template_kwargs"].(map[string]any); ok {
		out = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}
	}
	if _, ok := extra["enable_thinking"]; ok {
		if out == nil {
			out = map[string]any{}
		}
		out["enable_thinking"] = false
	}
	return out
}

// suggestCache keeps the reply suggestions of the limit chats most recently
// asked for, those of one message each, and each chat's generation in
// progress.
type suggestCache struct {
	mu      sync.Mutex
	limit   int
	entries map[string]*list.Element // of suggestEntry, by conversation key
	order   *list.List               // most recently used first
	flights map[string]*suggestFlight
}

type suggestEntry struct {
	convKey string
	seq     int64
	items   []string
}

// suggestFlight is a chat's generation of the suggestions of its message seq;
// done is closed once items is set.
type suggestFlight struct {
	seq   int64
	done  chan struct{}
	items []string
}

func newSuggestCache(limit int) *suggestCache {
	return &suggestCache{limit: limit, entries: map[string]*list.Element{}, order: list.New(),
		flights: map[string]*suggestFlight{}}
}

// join returns the cached suggestions of message seq of the chat convKey, with
// cached true; or else the chat's generation: the one in progress, or a new
// one for seq that the caller leads (lead true) and must finish.
func (c *suggestCache) join(convKey string, seq int64) (items []string, cached bool, f *suggestFlight, lead bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[convKey]; ok {
		if ent := e.Value.(suggestEntry); ent.seq == seq {
			c.order.MoveToFront(e)
			return slices.Clone(ent.items), true, nil, false
		}
	}
	if f := c.flights[convKey]; f != nil {
		return nil, false, f, false
	}
	f = &suggestFlight{seq: seq, done: make(chan struct{})}
	c.flights[convKey] = f
	return nil, false, f, true
}

// finish ends the generation f of the chat convKey with items, which replace
// the chat's cached suggestions unless empty: nothing usable is tried again
// on the next request. The least recently used chat is dropped when limit
// chats are cached.
func (c *suggestCache) finish(convKey string, f *suggestFlight, items []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(items) > 0 {
		ent := suggestEntry{convKey: convKey, seq: f.seq, items: items}
		if e, ok := c.entries[convKey]; ok {
			e.Value = ent
			c.order.MoveToFront(e)
		} else {
			if c.order.Len() >= c.limit {
				oldest := c.order.Back()
				c.order.Remove(oldest)
				delete(c.entries, oldest.Value.(suggestEntry).convKey)
			}
			c.entries[convKey] = c.order.PushFront(ent)
		}
	}
	f.items = items
	delete(c.flights, convKey)
	close(f.done)
}

// forget drops the chat's cached suggestions.
func (c *suggestCache) forget(convKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[convKey]; ok {
		c.order.Remove(e)
		delete(c.entries, convKey)
	}
}
