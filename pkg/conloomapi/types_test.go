package conloomapi

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestEventJSONRoundTripMatchesSpecExample(t *testing.T) {
	raw, err := os.ReadFile("testdata/event_example.json")
	if err != nil {
		t.Fatal(err)
	}
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	check := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	check("schema_version", ev.SchemaVersion, SchemaVersion)
	check("event_id", ev.EventID, "evt_01J...")
	check("event_type", ev.EventType, EventTypeMessageCreated)
	check("bot_instance_id", ev.BotInstanceID, "bot_main")
	check("platform", ev.Platform, "qq")
	check("conversation.type", ev.Conversation.Type, ConvGroup)
	check("conversation.id", ev.Conversation.ID, "group_123")
	check("conversation.platform_conversation_id", ev.Conversation.PlatformConversationID, "123456")
	check("actor.id", ev.Actor.ID, "user_456")
	check("actor.platform_user_id", ev.Actor.PlatformUserID, "456789")
	check("actor.display_name", ev.Actor.DisplayName, "小明")
	check("trigger.type", ev.Trigger.Type, TriggerMention)
	check("message.id", ev.Message.ID, "msg_789")
	check("message.text", ev.Message.Text, "还记得我们之前决定把服务迁到哪里吗？")
	if !reflect.DeepEqual(ev.Actor.Roles, []string{"member"}) {
		t.Errorf("actor.roles = %v", ev.Actor.Roles)
	}
	if !ev.Trigger.RequestedResponse {
		t.Error("trigger.requested_response = false")
	}
	if ev.Message.ReplyTo != nil {
		t.Errorf("message.reply_to = %q, want nil", *ev.Message.ReplyTo)
	}
	if !reflect.DeepEqual(ev.Message.Mentions, []Mention{{Type: "bot", ID: "bot_main"}}) {
		t.Errorf("message.mentions = %+v", ev.Message.Mentions)
	}
	if ev.Message.Attachments == nil || len(ev.Message.Attachments) != 0 {
		t.Errorf("message.attachments = %#v, want empty non-nil", ev.Message.Attachments)
	}
	if ev.Extensions == nil || len(ev.Extensions) != 0 {
		t.Errorf("extensions = %#v, want empty non-nil", ev.Extensions)
	}
	wantAt := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	if !ev.OccurredAt.Equal(wantAt) {
		t.Errorf("occurred_at = %v, want %v", ev.OccurredAt, wantAt)
	}

	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var want, got map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got  %s\n want %s", out, raw)
	}

	var again Event
	if err := json.Unmarshal(out, &again); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, ev) {
		t.Errorf("second decode differs:\n got  %+v\n want %+v", again, ev)
	}
}

func TestMessageReplyToAndPlatformMessageID(t *testing.T) {
	id := "msg_1"
	m := Message{ID: "msg_2", PlatformMessageID: "2", Text: "hi", ReplyTo: &id}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"reply_to":"msg_1"`, `"platform_message_id":"2"`} {
		if !strings.Contains(s, want) {
			t.Errorf("%s missing %s", s, want)
		}
	}
}

func TestEventAcceptedOmitsEmptyFields(t *testing.T) {
	b, err := json.Marshal(EventAccepted{EventID: "evt_1", Disposition: DispositionObserve})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"event_id":"evt_1","disposition":"observe"}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	b, err = json.Marshal(EventAccepted{EventID: "evt_1", Disposition: DispositionReply, TaskID: "task_1", Status: TaskQueued, Merged: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"event_id":"evt_1","disposition":"reply","task_id":"task_1","status":"queued","merged":true}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestErrorResponseShape(t *testing.T) {
	b, err := json.Marshal(ErrorResponse{Error: ErrorBody{Code: CodeInternal, Message: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":{"code":"INTERNAL_ERROR","message":"x","retryable":false,"retry_after_ms":0,"trace_id":""}}`
	if string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}

func TestDefaultPersonaValues(t *testing.T) {
	p := DefaultPersona()
	if p.PersonaName != "默认群友人格" {
		t.Errorf("PersonaName = %q", p.PersonaName)
	}
	if p.TemplateVersion != 1 {
		t.Errorf("TemplateVersion = %d", p.TemplateVersion)
	}
	wantStyle := PersonaStyle{Warmth: 0.75, Humor: 0.45, Directness: 0.65, Verbosity: 0.30, EmojiUsage: 0.15, Proactivity: 0.20}
	if p.Style != wantStyle {
		t.Errorf("Style = %+v, want %+v", p.Style, wantStyle)
	}
	wantBehavior := PersonaBehavior{DefaultReplyLength: "short", AllowMemes: true, AskFollowupQuestions: "occasionally", TechnicalExplanations: "clear_and_practical"}
	if p.Behavior != wantBehavior {
		t.Errorf("Behavior = %+v, want %+v", p.Behavior, wantBehavior)
	}
}

func TestDefaultGroupPolicyValues(t *testing.T) {
	p := DefaultGroupPolicy()
	c := p.Concurrency
	if c.MaxInteractiveRequests != 1 || c.MaxQueuedRequests != 10 || c.Priority != "normal" || !c.AllowLongRequests {
		t.Errorf("Concurrency = %+v", c)
	}
	r := p.Reply
	if r.CommandPrefix != "/" || r.ContinuationWindow.Duration != 0 || r.Debounce.Duration != 1500*time.Millisecond ||
		r.MinReplyInterval.Duration != 2*time.Second || r.UserRateLimitPerMinute != 6 {
		t.Errorf("Reply = %+v", r)
	}
	if p.Context.MaxRecentMessages != 50 || p.Context.MaxContextTokens != 0 {
		t.Errorf("Context = %+v", p.Context)
	}
	v := p.Review
	if v.Level != "basic" || !v.ReviewInput || !v.ReviewOutput || v.Action != "block" {
		t.Errorf("Review = %+v", v)
	}
}

func TestDurationJSONAndYAML(t *testing.T) {
	b, err := json.Marshal(Duration{1500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"1.5s"` {
		t.Errorf("json marshal = %s", b)
	}
	var d Duration
	if err := json.Unmarshal([]byte(`"1.5s"`), &d); err != nil || d.Duration != 1500*time.Millisecond {
		t.Errorf("json string: %v %v", d, err)
	}
	if err := json.Unmarshal([]byte(`2000`), &d); err != nil || d.Duration != 2*time.Second {
		t.Errorf("json int: %v %v", d, err)
	}
	if err := json.Unmarshal([]byte(`"bogus"`), &d); err == nil {
		t.Error("json bogus: want error")
	}
	if err := json.Unmarshal([]byte(`1.5`), &d); err == nil {
		t.Error("json float: want error")
	}

	y, err := yaml.Marshal(map[string]Duration{"d": {1500 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if string(y) != "d: 1.5s\n" {
		t.Errorf("yaml marshal = %q", y)
	}
	var m map[string]Duration
	if err := yaml.Unmarshal([]byte("a: 1.5s\nb: 2000\nc: \"3s\"\n"), &m); err != nil {
		t.Fatal(err)
	}
	if m["a"].Duration != 1500*time.Millisecond || m["b"].Duration != 2*time.Second || m["c"].Duration != 3*time.Second {
		t.Errorf("yaml unmarshal = %+v", m)
	}
	if err := yaml.Unmarshal([]byte("a: nope\n"), &m); err == nil {
		t.Error("yaml bogus: want error")
	}
}

func TestGroupPolicyYAMLTags(t *testing.T) {
	src := `
concurrency:
  max_interactive_requests: 2
  max_queued_requests: 5
  priority: high
  allow_long_requests: false
reply:
  command_prefix: "!"
  continuation_window: 30s
  debounce: 500
  min_reply_interval: 1s
  user_rate_limit_per_minute: 3
context:
  max_recent_messages: 20
  max_context_tokens: 4000
review:
  level: strict
  review_input: true
  review_output: false
  action: sanitize
  blocked_words: [foo]
`
	var p GroupPolicy
	if err := yaml.Unmarshal([]byte(src), &p); err != nil {
		t.Fatal(err)
	}
	want := GroupPolicy{
		Concurrency: ConcurrencyPolicy{MaxInteractiveRequests: 2, MaxQueuedRequests: 5, Priority: "high"},
		Reply: ReplyPolicy{CommandPrefix: "!", ContinuationWindow: Duration{30 * time.Second}, Debounce: Duration{500 * time.Millisecond},
			MinReplyInterval: Duration{time.Second}, UserRateLimitPerMinute: 3},
		Context: ContextPolicy{MaxRecentMessages: 20, MaxContextTokens: 4000},
		Review:  ReviewPolicy{Level: "strict", ReviewInput: true, Action: "sanitize", BlockedWords: []string{"foo"}},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
}

func TestGroupPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*GroupPolicy)
		wantErr string
	}{
		{"default", func(*GroupPolicy) {}, ""},
		{"concurrency zero ok", func(p *GroupPolicy) { p.Concurrency.MaxInteractiveRequests = 0 }, ""},
		{"concurrency two ok", func(p *GroupPolicy) { p.Concurrency.MaxInteractiveRequests = 2 }, ""},
		{"concurrency three", func(p *GroupPolicy) { p.Concurrency.MaxInteractiveRequests = 3 }, "concurrency.max_interactive_requests"},
		{"concurrency negative", func(p *GroupPolicy) { p.Concurrency.MaxInteractiveRequests = -1 }, "concurrency.max_interactive_requests"},
		{"queue zero", func(p *GroupPolicy) { p.Concurrency.MaxQueuedRequests = 0 }, "concurrency.max_queued_requests"},
		{"priority bad", func(p *GroupPolicy) { p.Concurrency.Priority = "urgent" }, "concurrency.priority"},
		{"priority low ok", func(p *GroupPolicy) { p.Concurrency.Priority = "low" }, ""},
		{"priority high ok", func(p *GroupPolicy) { p.Concurrency.Priority = "high" }, ""},
		{"debounce negative", func(p *GroupPolicy) { p.Reply.Debounce = Duration{-time.Millisecond} }, "reply.debounce"},
		{"debounce over 1m", func(p *GroupPolicy) { p.Reply.Debounce = Duration{time.Minute + time.Millisecond} }, "reply.debounce"},
		{"debounce 1m ok", func(p *GroupPolicy) { p.Reply.Debounce = Duration{time.Minute} }, ""},
		{"min interval over", func(p *GroupPolicy) { p.Reply.MinReplyInterval = Duration{2 * time.Minute} }, "reply.min_reply_interval"},
		{"continuation over", func(p *GroupPolicy) { p.Reply.ContinuationWindow = Duration{10*time.Minute + time.Second} }, "reply.continuation_window"},
		{"continuation negative", func(p *GroupPolicy) { p.Reply.ContinuationWindow = Duration{-time.Second} }, "reply.continuation_window"},
		{"rate limit zero", func(p *GroupPolicy) { p.Reply.UserRateLimitPerMinute = 0 }, "reply.user_rate_limit_per_minute"},
		{"recent negative", func(p *GroupPolicy) { p.Context.MaxRecentMessages = -1 }, "context.max_recent_messages"},
		{"recent over", func(p *GroupPolicy) { p.Context.MaxRecentMessages = 501 }, "context.max_recent_messages"},
		{"recent 500 ok", func(p *GroupPolicy) { p.Context.MaxRecentMessages = 500 }, ""},
		{"tokens negative", func(p *GroupPolicy) { p.Context.MaxContextTokens = -1 }, "context.max_context_tokens"},
		{"level bad", func(p *GroupPolicy) { p.Review.Level = "off" }, "review.level"},
		{"level strict ok", func(p *GroupPolicy) { p.Review.Level = "strict" }, ""},
		{"level standard ok", func(p *GroupPolicy) { p.Review.Level = "standard" }, ""},
		{"action bad", func(p *GroupPolicy) { p.Review.Action = "drop" }, "review.action"},
		{"action allow ok", func(p *GroupPolicy) { p.Review.Action = "allow" }, ""},
		{"action sanitize ok", func(p *GroupPolicy) { p.Review.Action = "sanitize" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultGroupPolicy()
			tt.mutate(&p)
			err := p.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPersonaDocValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PersonaDoc)
		wantErr string
	}{
		{"default", func(*PersonaDoc) {}, ""},
		{"name empty", func(p *PersonaDoc) { p.PersonaName = "" }, "persona_name"},
		{"name blank", func(p *PersonaDoc) { p.PersonaName = "  " }, "persona_name"},
		{"warmth over", func(p *PersonaDoc) { p.Style.Warmth = 1.2 }, "style.warmth"},
		{"humor negative", func(p *PersonaDoc) { p.Style.Humor = -0.1 }, "style.humor"},
		{"directness over", func(p *PersonaDoc) { p.Style.Directness = 1.01 }, "style.directness"},
		{"verbosity over", func(p *PersonaDoc) { p.Style.Verbosity = 2 }, "style.verbosity"},
		{"emoji negative", func(p *PersonaDoc) { p.Style.EmojiUsage = -1 }, "style.emoji_usage"},
		{"proactivity over", func(p *PersonaDoc) { p.Style.Proactivity = 1.5 }, "style.proactivity"},
		{"bounds ok", func(p *PersonaDoc) { p.Style.Warmth = 0; p.Style.Humor = 1 }, ""},
		{"reply length bad", func(p *PersonaDoc) { p.Behavior.DefaultReplyLength = "huge" }, "behavior.default_reply_length"},
		{"reply length long ok", func(p *PersonaDoc) { p.Behavior.DefaultReplyLength = "long" }, ""},
		{"reply length medium ok", func(p *PersonaDoc) { p.Behavior.DefaultReplyLength = "medium" }, ""},
		{"followup bad", func(p *PersonaDoc) { p.Behavior.AskFollowupQuestions = "always" }, "behavior.ask_followup_questions"},
		{"followup never ok", func(p *PersonaDoc) { p.Behavior.AskFollowupQuestions = "never" }, ""},
		{"followup often ok", func(p *PersonaDoc) { p.Behavior.AskFollowupQuestions = "often" }, ""},
		{"technical bad", func(p *PersonaDoc) { p.Behavior.TechnicalExplanations = "verbose" }, "behavior.technical_explanations"},
		{"technical brief ok", func(p *PersonaDoc) { p.Behavior.TechnicalExplanations = "brief" }, ""},
		{"technical detailed ok", func(p *PersonaDoc) { p.Behavior.TechnicalExplanations = "detailed" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultPersona()
			tt.mutate(&p)
			err := p.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
