package conloomapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefaultGroupPolicyProactive(t *testing.T) {
	want := ProactivePolicy{
		Enabled:                true,
		DefaultLevel:           ProactiveLow,
		EligibilityTTL:         Duration{720 * time.Hour},
		UserCooldown:           Duration{30 * time.Minute},
		GroupHourlyLimit:       4,
		DailyLimits:            ProactiveDailyLimits{Low: 2, Mid: 4, High: 8},
		JoinLull:               Duration{15 * time.Second},
		JoinWindow:             Duration{5 * time.Minute},
		JoinMinMessages:        4,
		JoinEvaluationsPerHour: 4,
		JoinCooldown:           Duration{10 * time.Minute},
	}
	if got := DefaultGroupPolicy().Proactive; got != want {
		t.Fatalf("Proactive = %+v, want %+v", got, want)
	}
}

func TestDefaultGroupPolicyParticipationFields(t *testing.T) {
	r := DefaultGroupPolicy().Reply
	if r.ContinuationWindow.Duration != 0 || r.TurnGap.Duration != 6*time.Second || r.TurnMaxWait.Duration != 20*time.Second ||
		r.TurnCooldown.Duration != 45*time.Second ||
		r.MaxUnpromptedReplies != 4 || r.MaxConsecutiveSkips != 2 || r.Quote != QuoteAuto {
		t.Fatalf("reply = %+v", r)
	}
	a := DefaultGroupPolicy().Proactive
	if a.JoinLull.Duration != 15*time.Second || a.JoinWindow.Duration != 5*time.Minute || a.JoinMinMessages != 4 ||
		a.JoinEvaluationsPerHour != 4 || a.JoinCooldown.Duration != 10*time.Minute {
		t.Fatalf("proactive = %+v", a)
	}
	if err := DefaultGroupPolicy().Validate(); err != nil {
		t.Fatalf("default policy invalid: %v", err)
	}

	b, err := json.Marshal(DefaultGroupPolicy())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"turn_gap":"6s"`, `"turn_max_wait":"20s"`, `"turn_cooldown":"45s"`, `"max_unprompted_replies":4`, `"max_consecutive_skips":2`,
		`"quote":"auto"`, `"join_lull":"15s"`, `"join_window":"5m0s"`, `"join_min_messages":4`, `"join_evaluations_per_hour":4`, `"join_cooldown":"10m0s"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("json %s missing %s", b, key)
		}
	}
	src := `
reply:
  turn_gap: 3s
  turn_cooldown: 90s
  turn_max_wait: 30s
  max_unprompted_replies: 5
  max_consecutive_skips: 3
  quote: never
proactive:
  join_lull: 20s
  join_window: 10m
  join_min_messages: 6
  join_evaluations_per_hour: 2
  join_cooldown: 30m
`
	var y GroupPolicy
	if err := yaml.Unmarshal([]byte(src), &y); err != nil {
		t.Fatal(err)
	}
	if y.Reply.TurnGap.Duration != 3*time.Second || y.Reply.TurnMaxWait.Duration != 30*time.Second || y.Reply.MaxUnpromptedReplies != 5 ||
		y.Reply.TurnCooldown.Duration != 90*time.Second ||
		y.Reply.MaxConsecutiveSkips != 3 || y.Reply.Quote != QuoteNever {
		t.Errorf("yaml reply = %+v", y.Reply)
	}
	if p := y.Proactive; p.JoinLull.Duration != 20*time.Second || p.JoinWindow.Duration != 10*time.Minute || p.JoinMinMessages != 6 ||
		p.JoinEvaluationsPerHour != 2 || p.JoinCooldown.Duration != 30*time.Minute {
		t.Errorf("yaml proactive = %+v", p)
	}
}

func TestValidateParticipationFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*GroupPolicy)
		wantErr string
	}{
		{"continuation 0 ok", func(p *GroupPolicy) { p.Reply.ContinuationWindow = Duration{0} }, ""},
		{"continuation 10m ok", func(p *GroupPolicy) { p.Reply.ContinuationWindow = Duration{10 * time.Minute} }, ""},
		{"continuation 11m", func(p *GroupPolicy) { p.Reply.ContinuationWindow = Duration{11 * time.Minute} }, "reply.continuation_window"},
		{"continuation negative", func(p *GroupPolicy) { p.Reply.ContinuationWindow = Duration{-time.Second} }, "reply.continuation_window"},
		{"debounce still 1m max", func(p *GroupPolicy) { p.Reply.Debounce = Duration{time.Minute + time.Second} }, "reply.debounce"},

		{"turn_gap 1s ok", func(p *GroupPolicy) { p.Reply.TurnGap = Duration{time.Second} }, ""},
		{"turn_gap 60s ok", func(p *GroupPolicy) {
			p.Reply.TurnGap = Duration{time.Minute}
			p.Reply.TurnMaxWait = Duration{time.Minute}
		}, ""},
		{"turn_gap under", func(p *GroupPolicy) { p.Reply.TurnGap = Duration{time.Second - time.Millisecond} }, "reply.turn_gap"},
		{"turn_gap zero", func(p *GroupPolicy) { p.Reply.TurnGap = Duration{0} }, "reply.turn_gap"},
		{"turn_gap over", func(p *GroupPolicy) {
			p.Reply.TurnGap = Duration{time.Minute + time.Second}
			p.Reply.TurnMaxWait = Duration{2 * time.Minute}
		}, "reply.turn_gap"},
		{"continuation above turn_cooldown ok", func(p *GroupPolicy) { p.Reply.ContinuationWindow = Duration{46 * time.Second} }, ""},
		{"continuation equal to turn_cooldown", func(p *GroupPolicy) { p.Reply.ContinuationWindow = Duration{45 * time.Second} }, "reply.continuation_window"},
		{"continuation below turn_cooldown", func(p *GroupPolicy) {
			p.Reply.ContinuationWindow = Duration{time.Minute}
			p.Reply.TurnCooldown = Duration{2 * time.Minute}
		}, "reply.continuation_window"},
		{"turn_cooldown above window off ok", func(p *GroupPolicy) { p.Reply.TurnCooldown = Duration{10 * time.Minute} }, ""},
		{"turn_cooldown 1s ok", func(p *GroupPolicy) { p.Reply.TurnCooldown = Duration{time.Second} }, ""},
		{"turn_cooldown 10m ok", func(p *GroupPolicy) { p.Reply.TurnCooldown = Duration{10 * time.Minute} }, ""},
		{"turn_cooldown under", func(p *GroupPolicy) { p.Reply.TurnCooldown = Duration{time.Second - time.Millisecond} }, "reply.turn_cooldown"},
		{"turn_cooldown zero", func(p *GroupPolicy) { p.Reply.TurnCooldown = Duration{0} }, "reply.turn_cooldown"},
		{"turn_cooldown over", func(p *GroupPolicy) { p.Reply.TurnCooldown = Duration{10*time.Minute + time.Second} }, "reply.turn_cooldown"},
		{"turn_max_wait equals gap ok", func(p *GroupPolicy) { p.Reply.TurnMaxWait = p.Reply.TurnGap }, ""},
		{"turn_max_wait 2m ok", func(p *GroupPolicy) { p.Reply.TurnMaxWait = Duration{2 * time.Minute} }, ""},
		{"turn_max_wait below gap", func(p *GroupPolicy) { p.Reply.TurnMaxWait = Duration{p.Reply.TurnGap.Duration - time.Millisecond} }, "reply.turn_max_wait"},
		{"turn_max_wait over", func(p *GroupPolicy) { p.Reply.TurnMaxWait = Duration{2*time.Minute + time.Second} }, "reply.turn_max_wait"},
		{"max_unprompted 1 ok", func(p *GroupPolicy) { p.Reply.MaxUnpromptedReplies = 1 }, ""},
		{"max_unprompted 20 ok", func(p *GroupPolicy) { p.Reply.MaxUnpromptedReplies = 20 }, ""},
		{"max_unprompted 0", func(p *GroupPolicy) { p.Reply.MaxUnpromptedReplies = 0 }, "reply.max_unprompted_replies"},
		{"max_unprompted 21", func(p *GroupPolicy) { p.Reply.MaxUnpromptedReplies = 21 }, "reply.max_unprompted_replies"},
		{"max_skips 1 ok", func(p *GroupPolicy) { p.Reply.MaxConsecutiveSkips = 1 }, ""},
		{"max_skips 10 ok", func(p *GroupPolicy) { p.Reply.MaxConsecutiveSkips = 10 }, ""},
		{"max_skips 0", func(p *GroupPolicy) { p.Reply.MaxConsecutiveSkips = 0 }, "reply.max_consecutive_skips"},
		{"max_skips 11", func(p *GroupPolicy) { p.Reply.MaxConsecutiveSkips = 11 }, "reply.max_consecutive_skips"},
		{"quote auto ok", func(p *GroupPolicy) { p.Reply.Quote = QuoteAuto }, ""},
		{"quote always ok", func(p *GroupPolicy) { p.Reply.Quote = QuoteAlways }, ""},
		{"quote never ok", func(p *GroupPolicy) { p.Reply.Quote = QuoteNever }, ""},
		{"quote empty", func(p *GroupPolicy) { p.Reply.Quote = "" }, "reply.quote"},
		{"quote bad", func(p *GroupPolicy) { p.Reply.Quote = "sometimes" }, "reply.quote"},

		{"join_lull 5s ok", func(p *GroupPolicy) { p.Proactive.JoinLull = Duration{5 * time.Second} }, ""},
		{"join_lull 5m ok", func(p *GroupPolicy) { p.Proactive.JoinLull = Duration{5 * time.Minute} }, ""},
		{"join_lull under", func(p *GroupPolicy) { p.Proactive.JoinLull = Duration{5*time.Second - time.Millisecond} }, "proactive.join_lull"},
		{"join_lull over", func(p *GroupPolicy) { p.Proactive.JoinLull = Duration{5*time.Minute + time.Second} }, "proactive.join_lull"},
		{"join_window 1m ok", func(p *GroupPolicy) { p.Proactive.JoinWindow = Duration{time.Minute} }, ""},
		{"join_window 30m ok", func(p *GroupPolicy) { p.Proactive.JoinWindow = Duration{30 * time.Minute} }, ""},
		{"join_window under", func(p *GroupPolicy) { p.Proactive.JoinWindow = Duration{time.Minute - time.Second} }, "proactive.join_window"},
		{"join_window over", func(p *GroupPolicy) { p.Proactive.JoinWindow = Duration{30*time.Minute + time.Second} }, "proactive.join_window"},
		{"join_min_messages 2 ok", func(p *GroupPolicy) { p.Proactive.JoinMinMessages = 2 }, ""},
		{"join_min_messages 50 ok", func(p *GroupPolicy) { p.Proactive.JoinMinMessages = 50 }, ""},
		{"join_min_messages 1", func(p *GroupPolicy) { p.Proactive.JoinMinMessages = 1 }, "proactive.join_min_messages"},
		{"join_min_messages 51", func(p *GroupPolicy) { p.Proactive.JoinMinMessages = 51 }, "proactive.join_min_messages"},
		{"join_evals 1 ok", func(p *GroupPolicy) { p.Proactive.JoinEvaluationsPerHour = 1 }, ""},
		{"join_evals 60 ok", func(p *GroupPolicy) { p.Proactive.JoinEvaluationsPerHour = 60 }, ""},
		{"join_evals 0", func(p *GroupPolicy) { p.Proactive.JoinEvaluationsPerHour = 0 }, "proactive.join_evaluations_per_hour"},
		{"join_evals 61", func(p *GroupPolicy) { p.Proactive.JoinEvaluationsPerHour = 61 }, "proactive.join_evaluations_per_hour"},
		{"join_cooldown 1m ok", func(p *GroupPolicy) { p.Proactive.JoinCooldown = Duration{time.Minute} }, ""},
		{"join_cooldown 24h ok", func(p *GroupPolicy) { p.Proactive.JoinCooldown = Duration{24 * time.Hour} }, ""},
		{"join_cooldown under", func(p *GroupPolicy) { p.Proactive.JoinCooldown = Duration{time.Minute - time.Second} }, "proactive.join_cooldown"},
		{"join_cooldown over", func(p *GroupPolicy) { p.Proactive.JoinCooldown = Duration{24*time.Hour + time.Second} }, "proactive.join_cooldown"},
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

func TestWithDefaultsFillsNewFieldsIndividually(t *testing.T) {
	def := DefaultGroupPolicy()

	stored := DefaultGroupPolicy()
	stored.Concurrency.MaxInteractiveRequests = 2
	stored.Reply.ContinuationWindow = Duration{time.Minute}
	stored.Reply.Debounce = Duration{3 * time.Second}
	stored.Proactive.Enabled = false
	stored.Proactive.DefaultLevel = ProactiveMid
	stored.Proactive.UserCooldown = Duration{time.Hour}
	stored.Proactive.DailyLimits = ProactiveDailyLimits{1, 2, 3}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"turn_gap", "turn_max_wait", "turn_cooldown", "max_unprompted_replies", "max_consecutive_skips", "quote"} {
		delete(doc["reply"], k)
	}
	for _, k := range []string{"join_lull", "join_window", "join_min_messages", "join_evaluations_per_hour", "join_cooldown"} {
		delete(doc["proactive"], k)
	}
	legacy, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "turn_") || strings.Contains(string(legacy), "join_") || strings.Contains(string(legacy), "quote") {
		t.Fatalf("legacy json still has new fields: %s", legacy)
	}

	var p GroupPolicy
	if err := json.Unmarshal(legacy, &p); err != nil {
		t.Fatal(err)
	}
	got := p.WithDefaults(def)
	if !reflect.DeepEqual(got, stored) {
		t.Fatalf("legacy read back = %+v\nwant %+v", got, stored)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("filled legacy policy invalid: %v", err)
	}

	custom := DefaultGroupPolicy()
	custom.Reply.TurnGap = Duration{2 * time.Second}
	custom.Reply.TurnCooldown = Duration{2 * time.Minute}
	custom.Reply.MaxConsecutiveSkips = 5
	custom.Reply.Quote = QuoteNever
	custom.Proactive.JoinCooldown = Duration{time.Hour}
	custom.Proactive.JoinMinMessages = 10
	if got := custom.WithDefaults(def); !reflect.DeepEqual(got, custom) {
		t.Fatalf("set fields changed: got %+v", got)
	}

	other := DefaultGroupPolicy()
	other.Reply.TurnMaxWait = Duration{time.Minute}
	other.Proactive.JoinLull = Duration{time.Minute}
	partial := DefaultGroupPolicy()
	partial.Reply.TurnMaxWait = Duration{0}
	other.Reply.Quote = QuoteAlways
	partial.Reply.ContinuationWindow = Duration{0}
	partial.Reply.Quote = ""
	partial.Proactive.JoinLull = Duration{0}
	got = partial.WithDefaults(other)
	if got.Reply.TurnMaxWait.Duration != time.Minute || got.Proactive.JoinLull.Duration != time.Minute || got.Reply.Quote != QuoteAlways {
		t.Fatalf("zero fields not filled from def: %+v %+v", got.Reply, got.Proactive)
	}
	if got.Reply.ContinuationWindow.Duration != 0 || got.Reply.TurnGap != def.Reply.TurnGap {
		t.Fatalf("other fields changed: %+v", got.Reply)
	}

	negative := DefaultGroupPolicy()
	negative.Reply.MaxUnpromptedReplies = -1
	if got := negative.WithDefaults(def); got.Reply.MaxUnpromptedReplies != -1 {
		t.Fatalf("negative value replaced: %d", got.Reply.MaxUnpromptedReplies)
	}
}

func TestProactiveDailyLimit(t *testing.T) {
	p := DefaultGroupPolicy().Proactive
	for level, want := range map[string]int{
		ProactiveOff: 0, ProactiveLow: 2, ProactiveMid: 4, ProactiveHigh: 8, "": 0, "max": 0,
	} {
		if got := p.DailyLimit(level); got != want {
			t.Errorf("DailyLimit(%q) = %d, want %d", level, got, want)
		}
	}
}

func TestProactiveLevelConstants(t *testing.T) {
	if ProactiveOff != "off" || ProactiveLow != "low" || ProactiveMid != "mid" || ProactiveHigh != "high" {
		t.Fatal("proactive level constants changed")
	}
	if TriggerContinuation != "continuation" || TriggerProactive != "proactive" {
		t.Fatal("trigger constants changed")
	}
}

func TestGroupPolicyValidateProactive(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ProactivePolicy)
		wantErr string
	}{
		{"default", func(*ProactivePolicy) {}, ""},
		{"disabled ok", func(p *ProactivePolicy) { p.Enabled = false }, ""},
		{"level off ok", func(p *ProactivePolicy) { p.DefaultLevel = ProactiveOff }, ""},
		{"level mid ok", func(p *ProactivePolicy) { p.DefaultLevel = ProactiveMid }, ""},
		{"level high ok", func(p *ProactivePolicy) { p.DefaultLevel = ProactiveHigh }, ""},
		{"level empty", func(p *ProactivePolicy) { p.DefaultLevel = "" }, "proactive.default_level"},
		{"level bad", func(p *ProactivePolicy) { p.DefaultLevel = "max" }, "proactive.default_level"},
		{"ttl 1h ok", func(p *ProactivePolicy) { p.EligibilityTTL = Duration{time.Hour} }, ""},
		{"ttl 8760h ok", func(p *ProactivePolicy) { p.EligibilityTTL = Duration{8760 * time.Hour} }, ""},
		{"ttl under 1h", func(p *ProactivePolicy) { p.EligibilityTTL = Duration{time.Hour - time.Second} }, "proactive.eligibility_ttl"},
		{"ttl zero", func(p *ProactivePolicy) { p.EligibilityTTL = Duration{0} }, "proactive.eligibility_ttl"},
		{"ttl over", func(p *ProactivePolicy) { p.EligibilityTTL = Duration{8760*time.Hour + time.Second} }, "proactive.eligibility_ttl"},
		{"cooldown zero ok", func(p *ProactivePolicy) { p.UserCooldown = Duration{0} }, ""},
		{"cooldown 24h ok", func(p *ProactivePolicy) { p.UserCooldown = Duration{24 * time.Hour} }, ""},
		{"cooldown negative", func(p *ProactivePolicy) { p.UserCooldown = Duration{-time.Second} }, "proactive.user_cooldown"},
		{"cooldown over", func(p *ProactivePolicy) { p.UserCooldown = Duration{24*time.Hour + time.Second} }, "proactive.user_cooldown"},
		{"hourly zero ok", func(p *ProactivePolicy) { p.GroupHourlyLimit = 0 }, ""},
		{"hourly 60 ok", func(p *ProactivePolicy) { p.GroupHourlyLimit = 60 }, ""},
		{"hourly negative", func(p *ProactivePolicy) { p.GroupHourlyLimit = -1 }, "proactive.group_hourly_limit"},
		{"hourly over", func(p *ProactivePolicy) { p.GroupHourlyLimit = 61 }, "proactive.group_hourly_limit"},
		{"daily all zero ok", func(p *ProactivePolicy) { p.DailyLimits = ProactiveDailyLimits{} }, ""},
		{"daily all 100 ok", func(p *ProactivePolicy) { p.DailyLimits = ProactiveDailyLimits{100, 100, 100} }, ""},
		{"daily low negative", func(p *ProactivePolicy) { p.DailyLimits.Low = -1 }, "proactive.daily_limits.low"},
		{"daily mid negative", func(p *ProactivePolicy) { p.DailyLimits = ProactiveDailyLimits{-1, -1, 8} }, "proactive.daily_limits.mid"},
		{"daily high over", func(p *ProactivePolicy) { p.DailyLimits.High = 101 }, "proactive.daily_limits.high"},
		{"daily low over mid", func(p *ProactivePolicy) { p.DailyLimits = ProactiveDailyLimits{5, 4, 8} }, "proactive.daily_limits"},
		{"daily mid over high", func(p *ProactivePolicy) { p.DailyLimits = ProactiveDailyLimits{2, 9, 8} }, "proactive.daily_limits"},
		{"zero section", func(p *ProactivePolicy) { *p = ProactivePolicy{} }, "proactive.default_level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultGroupPolicy()
			tt.mutate(&p.Proactive)
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

func TestWithDefaultsFillsZeroProactiveOnly(t *testing.T) {
	def := DefaultGroupPolicy()

	p := DefaultGroupPolicy()
	p.Concurrency.MaxInteractiveRequests = 0
	p.Reply.Debounce = Duration{3 * time.Second}
	p.Proactive = ProactivePolicy{}
	got := p.WithDefaults(def)
	want := p
	want.Proactive = def.Proactive
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zero proactive: got %+v\nwant %+v", got, want)
	}

	custom := DefaultGroupPolicy()
	custom.Proactive.Enabled = false
	custom.Proactive.DailyLimits = ProactiveDailyLimits{}
	if got := custom.WithDefaults(def); !reflect.DeepEqual(got, custom) {
		t.Fatalf("non-zero proactive replaced: got %+v", got.Proactive)
	}

	other := def
	other.Proactive.DefaultLevel = ProactiveHigh
	if got := (GroupPolicy{}).WithDefaults(other); got.Proactive != other.Proactive || got.Concurrency != (ConcurrencyPolicy{}) {
		t.Fatalf("empty policy: got %+v", got)
	}
}

func TestGroupPolicyProactiveTags(t *testing.T) {
	src := `
proactive:
  enabled: true
  default_level: mid
  eligibility_ttl: 48h
  user_cooldown: 10m
  group_hourly_limit: 3
  daily_limits: {low: 1, mid: 2, high: 3}
`
	want := ProactivePolicy{
		Enabled: true, DefaultLevel: ProactiveMid, EligibilityTTL: Duration{48 * time.Hour},
		UserCooldown: Duration{10 * time.Minute}, GroupHourlyLimit: 3, DailyLimits: ProactiveDailyLimits{1, 2, 3},
	}
	var y GroupPolicy
	if err := yaml.Unmarshal([]byte(src), &y); err != nil {
		t.Fatal(err)
	}
	if y.Proactive != want {
		t.Fatalf("yaml proactive = %+v, want %+v", y.Proactive, want)
	}

	b, err := json.Marshal(GroupPolicy{Proactive: want})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"proactive":{`, `"enabled":true`, `"default_level":"mid"`, `"eligibility_ttl":"48h0m0s"`,
		`"user_cooldown":"10m0s"`, `"group_hourly_limit":3`, `"daily_limits":{"low":1,"mid":2,"high":3}`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("json %s missing %s", b, key)
		}
	}
	var j GroupPolicy
	if err := json.Unmarshal(b, &j); err != nil || j.Proactive != want {
		t.Fatalf("json round trip = %+v, %v", j.Proactive, err)
	}
}

func TestPolicyJSONWithContactCooldownStillDecodes(t *testing.T) {
	for _, contact := range []string{"", `"contact_cooldown":"30m0s",`} {
		legacy := `{"proactive":{"enabled":true,"default_level":"low","eligibility_ttl":"720h","user_cooldown":"30m",` +
			contact + `"group_hourly_limit":4,"daily_limits":{"low":2,"mid":4,"high":8}}}`
		var p GroupPolicy
		if err := json.Unmarshal([]byte(legacy), &p); err != nil {
			t.Fatalf("%s: %v", legacy, err)
		}
		p = p.WithDefaults(DefaultGroupPolicy())
		if p.Proactive != DefaultGroupPolicy().Proactive {
			t.Fatalf("%s: proactive = %+v, want the defaults", legacy, p.Proactive)
		}
		full := DefaultGroupPolicy()
		full.Proactive = p.Proactive
		if err := full.Validate(); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(full)
		if err != nil || strings.Contains(string(b), "contact_cooldown") {
			t.Fatalf("re-encoded policy = %s, %v; want no contact_cooldown", b, err)
		}
	}
}

func TestPolicyPutUnmarshalRecordsProactivePresence(t *testing.T) {
	full, err := json.Marshal(PolicyPut{Policy: DefaultGroupPolicy(), Reason: "r"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Policy map[string]json.RawMessage `json:"policy"`
		Reason string                     `json:"reason"`
	}
	if err := json.Unmarshal(full, &doc); err != nil || doc.Reason != "r" {
		t.Fatalf("decode %s: %v", full, err)
	}
	delete(doc.Policy, "proactive")
	withoutProactive, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Policy["proactive"] = json.RawMessage(`{"enabled":false}`)
	disabledOnly, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Policy["proactive"] = json.RawMessage(`null`)
	nullProactive, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name        string
		body        []byte
		wantPresent bool
		wantOmitted bool
		want        ProactivePolicy
	}{
		{"full", full, true, false, DefaultGroupPolicy().Proactive},
		{"absent", withoutProactive, false, true, ProactivePolicy{}},
		{"enabled false only", disabledOnly, true, false, ProactivePolicy{}},
		{"null", nullProactive, false, true, ProactivePolicy{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var put PolicyPut
			if err := json.Unmarshal(tc.body, &put); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if put.ProactivePresent != tc.wantPresent || put.ProactiveOmitted() != tc.wantOmitted || put.Policy.Proactive != tc.want {
				t.Fatalf("put = present %v omitted %v proactive %+v", put.ProactivePresent, put.ProactiveOmitted(), put.Policy.Proactive)
			}
			want := DefaultGroupPolicy()
			want.Proactive = tc.want
			if !reflect.DeepEqual(put.Policy, want) || put.Reason != "r" {
				t.Fatalf("put = %+v", put)
			}
		})
	}

	if err := json.Unmarshal([]byte(`{"policy":{"proactive":{"enabled":"yes"}}}`), new(PolicyPut)); err == nil {
		t.Fatal("bad proactive type: err = nil")
	}
}

func TestPolicyPutProactiveOmittedInGo(t *testing.T) {
	if !(PolicyPut{}).ProactiveOmitted() {
		t.Fatal("zero section without presence flag must count as omitted")
	}
	if (PolicyPut{ProactivePresent: true}).ProactiveOmitted() {
		t.Fatal("present zero section must not count as omitted")
	}
	off := PolicyPut{Policy: DefaultGroupPolicy()}
	off.Policy.Proactive.Enabled = false
	if off.ProactiveOmitted() {
		t.Fatal("non-zero section must not count as omitted")
	}
}

func TestPolicyPutMarshalWireFormat(t *testing.T) {
	put := PolicyPut{Policy: DefaultGroupPolicy(), Reason: "r"}
	got, err := json.Marshal(put)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(struct {
		Policy GroupPolicy `json:"policy"`
		Reason string      `json:"reason"`
	}{put.Policy, put.Reason})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("marshal = %s\nwant %s", got, want)
	}
	var back PolicyPut
	if err := json.Unmarshal(got, &back); err != nil || !back.ProactivePresent || !reflect.DeepEqual(back.Policy, put.Policy) {
		t.Fatalf("round trip = %+v, %v", back, err)
	}

	omitted := put
	omitted.Policy.Proactive = ProactivePolicy{}
	got, err = json.Marshal(omitted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "proactive") {
		t.Fatalf("omitted section marshalled: %s", got)
	}
	back = PolicyPut{}
	if err := json.Unmarshal(got, &back); err != nil || !back.ProactiveOmitted() || !reflect.DeepEqual(back.Policy, omitted.Policy) {
		t.Fatalf("omitted round trip = %+v, %v", back, err)
	}

	omitted.ProactivePresent = true
	if got, _ = json.Marshal(omitted); !strings.Contains(string(got), `"proactive":{"enabled":false`) {
		t.Fatalf("present zero section not marshalled: %s", got)
	}
}

func TestDefaultGroupPolicyMemory(t *testing.T) {
	want := MemoryPolicy{
		Enabled:             false,
		Affinity:            true,
		ConsolidateEvery:    150,
		ConsolidateInterval: Duration{6 * time.Hour},
		ConsolidateMin:      20,
		Backlog:             1000,
	}
	if got := DefaultMemoryPolicy(); got != want {
		t.Fatalf("DefaultMemoryPolicy() = %+v, want %+v", got, want)
	}
	if got := DefaultGroupPolicy().Memory; got != want {
		t.Fatalf("DefaultGroupPolicy().Memory = %+v, want %+v", got, want)
	}
	b, err := json.Marshal(DefaultGroupPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if want := `"memory":{"enabled":false,"affinity":true,"consolidate_every":150,"consolidate_interval":"6h0m0s","consolidate_min":20,"backlog":1000,"summary_max_chars":0,"note_max_chars":0}`; !strings.Contains(string(b), want) {
		t.Fatalf("json %s missing %s", b, want)
	}
	var y GroupPolicy
	src := "memory:\n  enabled: true\n  affinity: false\n  consolidate_every: 300\n  consolidate_interval: 12h\n  backlog: 0\n" +
		"  summary_max_chars: 2500\n  note_max_chars: 600\n"
	if err := yaml.Unmarshal([]byte(src), &y); err != nil {
		t.Fatal(err)
	}
	if want := (MemoryPolicy{Enabled: true, ConsolidateEvery: 300, ConsolidateInterval: Duration{12 * time.Hour},
		SummaryMaxChars: 2500, NoteMaxChars: 600}); y.Memory != want {
		t.Fatalf("yaml memory = %+v, want %+v", y.Memory, want)
	}
}

func TestValidateMemory_Bounds(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*MemoryPolicy)
		wantErr string
	}{
		{"default", func(*MemoryPolicy) {}, ""},
		{"every 29", func(m *MemoryPolicy) { m.ConsolidateEvery = 29 }, "memory.consolidate_every must be in [30, 2000]"},
		{"every 30 ok", func(m *MemoryPolicy) { m.ConsolidateEvery = 30 }, ""},
		{"every 2000 ok", func(m *MemoryPolicy) { m.ConsolidateEvery = 2000 }, ""},
		{"every 2001", func(m *MemoryPolicy) { m.ConsolidateEvery = 2001 }, "memory.consolidate_every must be in [30, 2000]"},
		{"interval 29m", func(m *MemoryPolicy) { m.ConsolidateInterval = Duration{29 * time.Minute} }, "memory.consolidate_interval must be in [30m, 48h]"},
		{"interval 30m ok", func(m *MemoryPolicy) { m.ConsolidateInterval = Duration{30 * time.Minute} }, ""},
		{"interval 48h ok", func(m *MemoryPolicy) { m.ConsolidateInterval = Duration{48 * time.Hour} }, ""},
		{"interval 49h", func(m *MemoryPolicy) { m.ConsolidateInterval = Duration{49 * time.Hour} }, "memory.consolidate_interval must be in [30m, 48h]"},
		{"backlog -1", func(m *MemoryPolicy) { m.Backlog = -1 }, "memory.backlog must be in [0, 5000]"},
		{"backlog 0 ok", func(m *MemoryPolicy) { m.Backlog = 0 }, ""},
		{"backlog 5000 ok", func(m *MemoryPolicy) { m.Backlog = 5000 }, ""},
		{"backlog 5001", func(m *MemoryPolicy) { m.Backlog = 5001 }, "memory.backlog must be in [0, 5000]"},
		{"min 0", func(m *MemoryPolicy) { m.ConsolidateMin = 0 }, "memory.consolidate_min must be in [1, 2000]"},
		{"min 1 ok", func(m *MemoryPolicy) { m.ConsolidateMin = 1 }, ""},
		{"min 2000 ok", func(m *MemoryPolicy) { m.ConsolidateMin = 2000 }, ""},
		{"min 2001", func(m *MemoryPolicy) { m.ConsolidateMin = 2001 }, "memory.consolidate_min must be in [1, 2000]"},
		{"zero section", func(m *MemoryPolicy) { *m = MemoryPolicy{} }, "memory.consolidate_every"},
		{"summary 0 ok", func(m *MemoryPolicy) { m.SummaryMaxChars = 0 }, ""},
		{"summary -1", func(m *MemoryPolicy) { m.SummaryMaxChars = -1 }, "memory.summary_max_chars must be 0 or in [200, 8000]"},
		{"summary 199", func(m *MemoryPolicy) { m.SummaryMaxChars = 199 }, "memory.summary_max_chars must be 0 or in [200, 8000]"},
		{"summary 200 ok", func(m *MemoryPolicy) { m.SummaryMaxChars = 200 }, ""},
		{"summary 8000 ok", func(m *MemoryPolicy) { m.SummaryMaxChars = 8000 }, ""},
		{"summary 8001", func(m *MemoryPolicy) { m.SummaryMaxChars = 8001 }, "memory.summary_max_chars must be 0 or in [200, 8000]"},
		{"note 0 ok", func(m *MemoryPolicy) { m.NoteMaxChars = 0 }, ""},
		{"note -1", func(m *MemoryPolicy) { m.NoteMaxChars = -1 }, "memory.note_max_chars must be 0 or in [50, 2000]"},
		{"note 49", func(m *MemoryPolicy) { m.NoteMaxChars = 49 }, "memory.note_max_chars must be 0 or in [50, 2000]"},
		{"note 50 ok", func(m *MemoryPolicy) { m.NoteMaxChars = 50 }, ""},
		{"note 2000 ok", func(m *MemoryPolicy) { m.NoteMaxChars = 2000 }, ""},
		{"note 2001", func(m *MemoryPolicy) { m.NoteMaxChars = 2001 }, "memory.note_max_chars must be 0 or in [50, 2000]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultGroupPolicy()
			tt.mutate(&p.Memory)
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

func TestWithDefaultsKeepsZeroMemoryLimits(t *testing.T) {
	def := DefaultGroupPolicy()
	def.Memory.SummaryMaxChars, def.Memory.NoteMaxChars = 1500, 300
	var stored GroupPolicy
	if err := json.Unmarshal([]byte(`{"memory":{"enabled":true,"affinity":true,"consolidate_every":150,"consolidate_interval":"6h0m0s","consolidate_min":20,"backlog":1000}}`), &stored); err != nil {
		t.Fatal(err)
	}
	if m := stored.WithDefaults(def).Memory; m.SummaryMaxChars != 0 || m.NoteMaxChars != 0 {
		t.Fatalf("stored memory without limits = %d/%d after WithDefaults, want 0/0 (the conversation type's default)", m.SummaryMaxChars, m.NoteMaxChars)
	}
	stored.Memory.SummaryMaxChars, stored.Memory.NoteMaxChars = 2500, 600
	if m := stored.WithDefaults(def).Memory; m.SummaryMaxChars != 2500 || m.NoteMaxChars != 600 {
		t.Fatalf("stored limits = %d/%d after WithDefaults, want 2500/600", m.SummaryMaxChars, m.NoteMaxChars)
	}
}

func TestWithDefaultsCopiesLimitsOfAMissingMemorySection(t *testing.T) {
	def := DefaultGroupPolicy()
	def.Memory.SummaryMaxChars, def.Memory.NoteMaxChars = 4000, 900
	var stored GroupPolicy
	if err := json.Unmarshal([]byte(`{"concurrency":{"max_interactive_requests":1}}`), &stored); err != nil {
		t.Fatal(err)
	}
	if m := stored.WithDefaults(def).Memory; m != def.Memory {
		t.Fatalf("missing memory section = %+v after WithDefaults, want def's whole section %+v", m, def.Memory)
	}
}

func TestValidateMemory_RequiresLongRequests(t *testing.T) {
	p := DefaultGroupPolicy()
	p.Concurrency.AllowLongRequests = false
	p.Memory.Enabled = true
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "memory.enabled requires concurrency.allow_long_requests") {
		t.Fatalf("enabled without long requests: err = %v", err)
	}
	p.Memory.Enabled = false
	if err := p.Validate(); err != nil {
		t.Fatalf("disabled without long requests: %v", err)
	}
	p.Concurrency.AllowLongRequests = true
	p.Memory.Enabled = true
	if err := p.Validate(); err != nil {
		t.Fatalf("enabled with long requests: %v", err)
	}
}

func TestMemoryPolicy_MissingKeysUseDefaults(t *testing.T) {
	var m MemoryPolicy
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &m); err != nil {
		t.Fatal(err)
	}
	want := DefaultMemoryPolicy()
	want.Enabled = true
	if m != want || !m.Affinity || m.Backlog != 1000 || m.ConsolidateEvery != 150 || m.ConsolidateMin != 20 {
		t.Fatalf("partial section = %+v, want %+v", m, want)
	}

	m = MemoryPolicy{}
	if err := json.Unmarshal([]byte(`{"enabled":true,"backlog":0,"affinity":false}`), &m); err != nil {
		t.Fatal(err)
	}
	want = DefaultMemoryPolicy()
	want.Enabled, want.Backlog, want.Affinity = true, 0, false
	if m != want {
		t.Fatalf("explicit zero values = %+v, want %+v", m, want)
	}

	var put PolicyPut
	if err := json.Unmarshal([]byte(`{"policy":{"memory":{"enabled":true}}}`), &put); err != nil {
		t.Fatal(err)
	}
	want = DefaultMemoryPolicy()
	want.Enabled = true
	if !put.MemoryPresent || put.MemoryOmitted() || put.Policy.Memory != want {
		t.Fatalf("put memory = %+v (present %v)", put.Policy.Memory, put.MemoryPresent)
	}

	var p GroupPolicy
	if err := json.Unmarshal([]byte(`{"memory":{"backlog":5}}`), &p); err != nil {
		t.Fatal(err)
	}
	want = DefaultMemoryPolicy()
	want.Backlog = 5
	if p.Memory != want {
		t.Fatalf("group policy memory = %+v, want %+v", p.Memory, want)
	}
	var legacy GroupPolicy
	if err := json.Unmarshal([]byte(`{"memory":{"enabled":true,"affinity":true,"consolidate_every":150,"consolidate_interval":"6h0m0s","backlog":1000}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Memory.ConsolidateMin != 20 {
		t.Fatalf("version stored before consolidate_min: consolidate_min = %d, want 20", legacy.Memory.ConsolidateMin)
	}
	p = GroupPolicy{}
	if err := json.Unmarshal([]byte(`{"memory":null}`), &p); err != nil || p.Memory != (MemoryPolicy{}) {
		t.Fatalf("null memory = %+v, %v; want zero", p.Memory, err)
	}

	if err := json.Unmarshal([]byte(`{"enabled":"yes"}`), new(MemoryPolicy)); err == nil {
		t.Fatal("bad memory type: err = nil")
	}
}

func TestWithDefaultsFillsMissingMemorySection(t *testing.T) {
	def := DefaultGroupPolicy()
	def.Memory = MemoryPolicy{Enabled: true, Affinity: false, ConsolidateEvery: 300, ConsolidateInterval: Duration{time.Hour}, Backlog: 50}

	raw, err := json.Marshal(DefaultGroupPolicy())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "memory")
	legacy, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var p GroupPolicy
	if err := json.Unmarshal(legacy, &p); err != nil {
		t.Fatal(err)
	}
	if p.Memory != (MemoryPolicy{}) {
		t.Fatalf("legacy memory decoded as %+v, want zero", p.Memory)
	}
	got := p.WithDefaults(def)
	if !reflect.DeepEqual(got, def) {
		t.Fatalf("legacy read back = %+v\nwant %+v", got, def)
	}

	custom := DefaultGroupPolicy()
	custom.Memory.Affinity = false
	custom.Memory.Backlog = 0
	if got := custom.WithDefaults(def); got.Memory != custom.Memory {
		t.Fatalf("non-zero memory replaced: got %+v", got.Memory)
	}
}

func TestTaskMetaTrigger(t *testing.T) {
	b, err := json.Marshal(TaskMeta{Trigger: TriggerProactive})
	if err != nil || string(b) != `{"trigger":"proactive"}` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	if b, _ := json.Marshal(TaskMeta{}); string(b) != `{}` {
		t.Fatalf("empty meta = %s", b)
	}
}

func TestDefaultGroupPolicyRhythmFields(t *testing.T) {
	r := DefaultGroupPolicy().Reply
	if r.MaxParts != 1 || r.PrivateWait.Duration != 6*time.Second || r.PrivateMaxWait.Duration != 20*time.Second {
		t.Fatalf("reply = %+v", r)
	}
	b, err := json.Marshal(DefaultGroupPolicy())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"max_parts":1`, `"private_wait":"6s"`, `"private_max_wait":"20s"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("json %s missing %s", b, key)
		}
	}
	src := `
reply:
  max_parts: 3
  private_wait: 2s
  private_max_wait: 30s
`
	var y GroupPolicy
	if err := yaml.Unmarshal([]byte(src), &y); err != nil {
		t.Fatal(err)
	}
	if y.Reply.MaxParts != 3 || y.Reply.PrivateWait.Duration != 2*time.Second || y.Reply.PrivateMaxWait.Duration != 30*time.Second {
		t.Errorf("yaml reply = %+v", y.Reply)
	}
}

func TestWithDefaultsFillsRhythmFields(t *testing.T) {
	def := DefaultGroupPolicy()
	def.Reply.MaxParts = 3
	def.Reply.PrivateWait = Duration{4 * time.Second}
	def.Reply.PrivateMaxWait = Duration{30 * time.Second}

	stored := DefaultGroupPolicy()
	stored.Reply.TurnGap = Duration{2 * time.Second}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"max_parts", "private_wait", "private_max_wait"} {
		delete(doc["reply"], k)
	}
	legacy, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "max_parts") || strings.Contains(string(legacy), "private_") {
		t.Fatalf("legacy json still has new fields: %s", legacy)
	}
	var p GroupPolicy
	if err := json.Unmarshal(legacy, &p); err != nil {
		t.Fatal(err)
	}
	got := p.WithDefaults(def)
	want := stored
	want.Reply.MaxParts = 3
	want.Reply.PrivateWait = def.Reply.PrivateWait
	want.Reply.PrivateMaxWait = def.Reply.PrivateMaxWait
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy read back = %+v\nwant %+v", got.Reply, want.Reply)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("filled legacy policy invalid: %v", err)
	}
	if got := p.WithDefaults(DefaultGroupPolicy()); got.Reply.MaxParts != 1 {
		t.Fatalf("legacy max_parts = %d, want 1", got.Reply.MaxParts)
	}

	custom := DefaultGroupPolicy()
	custom.Reply.MaxParts = 5
	custom.Reply.PrivateWait = Duration{10 * time.Second}
	custom.Reply.PrivateMaxWait = Duration{time.Minute}
	if got := custom.WithDefaults(def); !reflect.DeepEqual(got, custom) {
		t.Fatalf("set fields changed: got %+v", got.Reply)
	}

	noWait := DefaultGroupPolicy()
	noWait.Reply.PrivateWait = Duration{0}
	noWait.Reply.PrivateMaxWait = Duration{5 * time.Second}
	if got := noWait.WithDefaults(def); got.Reply.PrivateWait.Duration != 0 || got.Reply.PrivateMaxWait.Duration != 5*time.Second {
		t.Fatalf("explicit zero private_wait replaced: %+v", got.Reply)
	}

	badMax := DefaultGroupPolicy()
	badMax.Reply.PrivateWait = Duration{10 * time.Second}
	badMax.Reply.PrivateMaxWait = Duration{0}
	got = badMax.WithDefaults(def)
	if got.Reply.PrivateWait.Duration != 10*time.Second || got.Reply.PrivateMaxWait.Duration != 0 {
		t.Fatalf("partly set private waits changed: %+v", got.Reply)
	}
	if err := got.Validate(); err == nil || !strings.Contains(err.Error(), "reply.private_max_wait") {
		t.Fatalf("err = %v, want reply.private_max_wait", err)
	}

	negative := DefaultGroupPolicy()
	negative.Reply.MaxParts = -1
	if got := negative.WithDefaults(def); got.Reply.MaxParts != -1 {
		t.Fatalf("negative max_parts replaced: %d", got.Reply.MaxParts)
	}
}

func TestValidateRhythmFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*GroupPolicy)
		wantErr string
	}{
		{"max_parts 0", func(p *GroupPolicy) { p.Reply.MaxParts = 0 }, "reply.max_parts"},
		{"max_parts 1 ok", func(p *GroupPolicy) { p.Reply.MaxParts = 1 }, ""},
		{"max_parts 5 ok", func(p *GroupPolicy) { p.Reply.MaxParts = 5 }, ""},
		{"max_parts 6", func(p *GroupPolicy) { p.Reply.MaxParts = 6 }, "reply.max_parts"},

		{"private_wait negative", func(p *GroupPolicy) { p.Reply.PrivateWait = Duration{-time.Nanosecond} }, "reply.private_wait"},
		{"private_wait 0 ok", func(p *GroupPolicy) { p.Reply.PrivateWait = Duration{0} }, ""},
		{"private_wait 60s ok", func(p *GroupPolicy) {
			p.Reply.PrivateWait = Duration{time.Minute}
			p.Reply.PrivateMaxWait = Duration{time.Minute}
		}, ""},
		{"private_wait 61s", func(p *GroupPolicy) {
			p.Reply.PrivateWait = Duration{61 * time.Second}
			p.Reply.PrivateMaxWait = Duration{2 * time.Minute}
		}, "reply.private_wait"},

		{"private_max_wait equals wait ok", func(p *GroupPolicy) { p.Reply.PrivateMaxWait = p.Reply.PrivateWait }, ""},
		{"private_max_wait 2m ok", func(p *GroupPolicy) { p.Reply.PrivateMaxWait = Duration{2 * time.Minute} }, ""},
		{"private_max_wait below wait", func(p *GroupPolicy) {
			p.Reply.PrivateMaxWait = Duration{p.Reply.PrivateWait.Duration - time.Millisecond}
		}, "reply.private_max_wait"},
		{"private_max_wait over 2m", func(p *GroupPolicy) { p.Reply.PrivateMaxWait = Duration{2*time.Minute + time.Second} }, "reply.private_max_wait"},
		{"private waits both 0 ok", func(p *GroupPolicy) {
			p.Reply.PrivateWait = Duration{0}
			p.Reply.PrivateMaxWait = Duration{0}
		}, ""},
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
