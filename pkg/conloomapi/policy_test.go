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
		Enabled:          true,
		DefaultLevel:     ProactiveLow,
		EligibilityTTL:   Duration{720 * time.Hour},
		UserCooldown:     Duration{30 * time.Minute},
		GroupHourlyLimit: 4,
		DailyLimits:      ProactiveDailyLimits{Low: 2, Mid: 4, High: 8},
	}
	if got := DefaultGroupPolicy().Proactive; got != want {
		t.Fatalf("Proactive = %+v, want %+v", got, want)
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

func TestTaskMetaTrigger(t *testing.T) {
	b, err := json.Marshal(TaskMeta{Trigger: TriggerProactive})
	if err != nil || string(b) != `{"trigger":"proactive"}` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	if b, _ := json.Marshal(TaskMeta{}); string(b) != `{}` {
		t.Fatalf("empty meta = %s", b)
	}
}
