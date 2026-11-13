package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "conloom.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	return cfg
}

func TestExpand(t *testing.T) {
	env := map[string]string{"A": "x", "EMPTY": ""}
	lookup := func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
	cases := []struct {
		in, want    string
		wantMissing []string
	}{
		{"${A}", "x", nil},
		{"${B:-d}", "d", nil},
		{"${A:-d}", "x", nil},
		{"${EMPTY:-d}", "d", nil},
		{"${EMPTY}", "", nil},
		{"${C}", "", []string{"C"}},
		{"$$x", "$x", nil},
		{"$${A}", "${A}", nil},
		{"a${A}b", "axb", nil},
		{"${C}-${D}-${C}", "--", []string{"C", "D", "C"}},
		{"cost $5", "cost $5", nil},
		{"${unterminated", "${unterminated", nil},
		{"plain", "plain", nil},
	}
	for _, c := range cases {
		got, missing := Expand(c.in, lookup)
		if got != c.want || !slices.Equal(missing, c.wantMissing) {
			t.Errorf("Expand(%q) = %q, %v; want %q, %v", c.in, got, missing, c.want, c.wantMissing)
		}
	}
}

var exampleEnv = map[string]string{
	"CONLOOM_SHORT_MODEL_ENDPOINT": "http://127.0.0.1:9/v1",
	"CONLOOM_SHORT_MODEL_NAME":     "fake-short",
	"CONLOOM_SHORT_MODEL_KEY":      "fake-short-key",
	"CONLOOM_LONG_MODEL_ENDPOINT":  "http://127.0.0.1:9/v1",
	"CONLOOM_LONG_MODEL_NAME":      "fake-long",
	"CONLOOM_LONG_MODEL_KEY":       "fake-long-key",
	"CONLOOM_ADAPTER_TOKEN":        "fake-adapter-token",
	"CONLOOM_ADMIN_TOKEN":          "fake-admin-token",
	"CONLOOM_BOT_SELF_ID":          "30001",
	"CONLOOM_ADMIN_QQ":             "29999",
	"CONLOOM_GROUP_ID":             "10001",
	"CONLOOM_NAPCAT_WS_URL":        "ws://127.0.0.1:9",
	"CONLOOM_NAPCAT_TOKEN":         "fake-napcat-token",
}

func TestLoad_ExampleFileParsesWithEnv(t *testing.T) {
	for k, v := range exampleEnv {
		t.Setenv(k, v)
	}
	cfg := mustLoad(t, "../../conloom.example.yaml")
	if len(cfg.MissingEnv) != 0 {
		t.Fatalf("MissingEnv = %v", cfg.MissingEnv)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	if err := cfg.ValidateNapCat(); err != nil {
		t.Fatalf("ValidateNapCat: %v", err)
	}
	if err := cfg.ValidateMock(); err != nil {
		t.Fatalf("ValidateMock: %v", err)
	}

	bot, ok := cfg.Bot("bot_main")
	if !ok {
		t.Fatal("bot_main not found")
	}
	if bot.Platform != "qq" || bot.Name != "织语" || bot.PlatformSelfID != "30001" {
		t.Errorf("bot = %+v", bot)
	}
	if !slices.Equal(bot.Admins, []string{"29999"}) {
		t.Errorf("admins = %v", bot.Admins)
	}
	wantConv := []conloomapi.ConversationRef{{Type: "group", PlatformID: "10001"}}
	if !reflect.DeepEqual(bot.EnabledConversations, wantConv) {
		t.Errorf("enabled_conversations = %+v", bot.EnabledConversations)
	}

	short := cfg.ModelProfiles["short_context"]
	if short.Provider != "openai_compatible" || short.Endpoint != "http://127.0.0.1:9/v1" || short.Model != "fake-short" || short.APIKey != "fake-short-key" {
		t.Errorf("short_context = %+v", short)
	}
	wantShortCtx := ContextLimits{NativeWindow: 32768, ConfiguredWindow: 32768, SoftInputBudget: 16000, ReservedOutputTokens: 1024, SafetyMarginTokens: 1024}
	if short.Context != wantShortCtx {
		t.Errorf("short_context.context = %+v", short.Context)
	}
	if !slices.Equal(short.Modalities, []string{"text"}) || short.Runtime.MaxConcurrency != 2 {
		t.Errorf("short_context modalities/runtime = %v %+v", short.Modalities, short.Runtime)
	}
	if short.Privacy != (Privacy{DataLocation: "remote", AllowSensitiveContent: true}) {
		t.Errorf("short_context.privacy = %+v", short.Privacy)
	}
	long := cfg.ModelProfiles["long_context"]
	wantLongCtx := ContextLimits{NativeWindow: 1000000, ConfiguredWindow: 1000000, SoftInputBudget: 120000, ReservedOutputTokens: 4096, SafetyMarginTokens: 4096}
	if long.Context != wantLongCtx || long.Runtime.MaxConcurrency != 2 {
		t.Errorf("long_context = %+v", long)
	}

	if len(cfg.APITokens) != 2 {
		t.Fatalf("api_tokens = %+v", cfg.APITokens)
	}
	for _, tok := range cfg.APITokens {
		switch tok.Name {
		case "napcat":
			want := []string{"events:write", "tasks:read", "outbox:read", "outbox:ack", "persona:read"}
			if tok.Token != "fake-adapter-token" || !slices.Equal(tok.Scopes, want) {
				t.Errorf("napcat token = %+v", tok)
			}
		case "admin":
			if tok.Token != "fake-admin-token" || !slices.Equal(tok.Scopes, allScopes) {
				t.Errorf("admin token = %+v", tok)
			}
		default:
			t.Errorf("unexpected token %q", tok.Name)
		}
	}

	if !reflect.DeepEqual(cfg.PersonaTemplates["default"], conloomapi.DefaultPersona()) {
		t.Errorf("persona_templates.default = %+v, want the built-in default", cfg.PersonaTemplates["default"])
	}
	lib, ok := cfg.PersonaTemplates["librarian"]
	if !ok || lib.CharacterName != "砚秋" || !strings.Contains(strings.TrimSpace(lib.Description), "\n") ||
		lib.SpeakingStyle == "" || len(lib.Examples) < 2 {
		t.Errorf("persona_templates.librarian = %+v, want a character template with multi-line text and examples", lib)
	}
	if got, want := cfg.DefaultGroupPolicy.Proactive, conloomapi.DefaultGroupPolicy().Proactive; got != want {
		t.Errorf("default_group_policy.proactive = %+v, want %+v", got, want)
	}
	if got, want := cfg.DefaultGroupPolicy.Reply, conloomapi.DefaultGroupPolicy().Reply; got != want {
		t.Errorf("default_group_policy.reply = %+v, want %+v", got, want)
	}

	nc := cfg.Adapters.NapCat
	if nc.CoreURL != "http://127.0.0.1:8787" || nc.WSURL != "ws://127.0.0.1:9" || nc.AccessToken != "fake-napcat-token" {
		t.Errorf("napcat = %+v", nc)
	}
}

func TestLoad_ExampleConfigMemoryDisabled(t *testing.T) {
	for k, v := range exampleEnv {
		t.Setenv(k, v)
	}
	gp := mustLoad(t, "../../conloom.example.yaml").DefaultGroupPolicy
	if gp.Memory.Enabled {
		t.Fatal("default_group_policy.memory.enabled = true, want false")
	}
	if want := conloomapi.DefaultMemoryPolicy(); gp.Memory != want {
		t.Errorf("default_group_policy.memory = %+v, want %+v", gp.Memory, want)
	}
}

func TestLoad_DotEnvDoesNotOverrideProcessEnv(t *testing.T) {
	path := writeConfig(t, `bots:
  - id: bot_main
    platform: qq
    name: ${CONLOOM_TEST_DOTENV_A}
    platform_self_id: ${CONLOOM_TEST_DOTENV_B}
    admins: ["${CONLOOM_TEST_DOTENV_C}"]
`)
	dotenv := "# comment\n\nCONLOOM_TEST_DOTENV_A=from-file\nexport CONLOOM_TEST_DOTENV_B=\"30001\"\nCONLOOM_TEST_DOTENV_C='29999'\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".env"), []byte(dotenv), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONLOOM_TEST_DOTENV_A", "from-process")

	cfg := mustLoad(t, path)
	bot := cfg.Bots[0]
	if bot.Name != "from-process" {
		t.Errorf("name = %q, want process env to win", bot.Name)
	}
	if bot.PlatformSelfID != "30001" {
		t.Errorf("platform_self_id = %q, want value from .env", bot.PlatformSelfID)
	}
	if !slices.Equal(bot.Admins, []string{"29999"}) {
		t.Errorf("admins = %v", bot.Admins)
	}
	if len(cfg.MissingEnv) != 0 {
		t.Errorf("MissingEnv = %v", cfg.MissingEnv)
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	cfg := mustLoad(t, "testdata/minimal.yaml")

	absPath, err := filepath.Abs("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != absPath {
		t.Errorf("Path = %q, want %q", cfg.Path, absPath)
	}
	if cfg.Server != (ServerConfig{Listen: "127.0.0.1:8787", Timezone: "Asia/Shanghai", MetricsEnabled: true}) {
		t.Errorf("server = %+v", cfg.Server)
	}
	wantData := filepath.Join(filepath.Dir(absPath), "data")
	if cfg.Storage.DataDir != wantData {
		t.Errorf("DataDir = %q, want %q", cfg.Storage.DataDir, wantData)
	}
	if cfg.DBPath() != filepath.Join(wantData, "conloom.db") {
		t.Errorf("DBPath = %q", cfg.DBPath())
	}
	if cfg.Adapters.NapCat.StateDir != wantData {
		t.Errorf("napcat.state_dir = %q, want %q", cfg.Adapters.NapCat.StateDir, wantData)
	}

	bot := cfg.Bots[0]
	if bot.Name != "织语" || bot.PrivateChat != "admins" || bot.PersonaTemplate != "default" {
		t.Errorf("bot defaults = %+v", bot)
	}

	wantRuntime := RuntimeConfig{
		ActionTTL:            conloomapi.Duration{Duration: 10 * time.Minute},
		OutboxLease:          conloomapi.Duration{Duration: 60 * time.Second},
		StaleTaskAfter:       conloomapi.Duration{Duration: 2 * time.Minute},
		IdempotencyRetention: conloomapi.Duration{Duration: 168 * time.Hour},
		MaxMergeWindow:       conloomapi.Duration{Duration: 4500 * time.Millisecond},
		SessionIdle:          conloomapi.Duration{Duration: 5 * time.Minute},
	}
	if cfg.Runtime != wantRuntime {
		t.Errorf("runtime = %+v", cfg.Runtime)
	}

	p := cfg.ModelProfiles["short_context"]
	wantRT := ProfileRuntime{MaxConcurrency: 2, Timeout: conloomapi.Duration{Duration: 60 * time.Second}, MaxRetries: 2}
	if p.Runtime != wantRT || p.TokenEstimateFactor != 1.0 || p.Provider != "openai_compatible" {
		t.Errorf("profile defaults = %+v factor=%v provider=%q", p.Runtime, p.TokenEstimateFactor, p.Provider)
	}

	if cfg.Observability != (ObservabilityConfig{LogLevel: "info", LogFormat: "json"}) {
		t.Errorf("observability = %+v", cfg.Observability)
	}
	if !reflect.DeepEqual(cfg.DefaultGroupPolicy, conloomapi.DefaultGroupPolicy()) {
		t.Errorf("default_group_policy = %+v", cfg.DefaultGroupPolicy)
	}
	if !reflect.DeepEqual(cfg.PersonaTemplates["default"], conloomapi.DefaultPersona()) {
		t.Errorf("persona_templates.default = %+v", cfg.PersonaTemplates["default"])
	}
}

func TestLoad_ExplicitValuesOverrideDefaults(t *testing.T) {
	path := writeConfig(t, `server:
  metrics_enabled: false
storage:
  data_dir: state
default_group_policy:
  concurrency:
    max_interactive_requests: 2
model_profiles:
  short_context:
    endpoint: http://127.0.0.1:9/v1
    model: fake-short
    provider: ${CONLOOM_TEST_UNSET_PROVIDER:-}
    api_key: ${CONLOOM_TEST_KEY}
    context:
      native_window: ${CONLOOM_TEST_WINDOW}
    runtime:
      max_retries: 0
      timeout: 5s
`)
	t.Setenv("CONLOOM_TEST_WINDOW", "4096")
	t.Setenv("CONLOOM_TEST_KEY", "null")
	cfg := mustLoad(t, path)

	if cfg.Server.MetricsEnabled || cfg.Server.Listen != "127.0.0.1:8787" {
		t.Errorf("server = %+v", cfg.Server)
	}
	if want := filepath.Join(filepath.Dir(path), "state"); cfg.Storage.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.Storage.DataDir, want)
	}
	gp := cfg.DefaultGroupPolicy
	if gp.Concurrency.MaxInteractiveRequests != 2 || gp.Concurrency.MaxQueuedRequests != 10 {
		t.Errorf("group policy concurrency = %+v", gp.Concurrency)
	}
	p := cfg.ModelProfiles["short_context"]
	if p.Runtime.MaxRetries != 0 || p.Runtime.Timeout.Duration != 5*time.Second || p.Runtime.MaxConcurrency != 2 {
		t.Errorf("profile runtime = %+v", p.Runtime)
	}
	if p.Provider != "openai_compatible" {
		t.Errorf("provider = %q, want empty value to default to openai_compatible", p.Provider)
	}
	if p.APIKey != "null" {
		t.Errorf("api_key = %q, want the literal expanded value", p.APIKey)
	}
	if p.Context.NativeWindow != 4096 {
		t.Errorf("native_window = %d, want env-expanded 4096", p.Context.NativeWindow)
	}
}

const textPersonaYAML = `
persona_templates:
  lighthouse:
    persona_name: 灯塔看守
    template_version: 1
    style: {warmth: 0.8, humor: 0.4, directness: 0.5, verbosity: 0.3, emoji_usage: 0.1, proactivity: 0.2}
    behavior:
      default_reply_length: short
      allow_memes: false
      ask_followup_questions: occasionally
      technical_explanations: brief
    character_name: NAME
    description: |
      苔灯是海边小镇的灯塔看守。
      她喜欢收集贝壳。
    speaking_style: >-
      说话轻快，
      句子短。
    examples:
      - user: 今天好累
        reply: |
          辛苦啦，
          歇一会儿吧。
`

func TestLoad_PersonaTemplateTextFields(t *testing.T) {
	base, err := os.ReadFile("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, writeConfig(t, string(base)+strings.Replace(textPersonaYAML, "NAME", "苔灯", 1)))
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	p := cfg.PersonaTemplates["lighthouse"]
	want := []conloomapi.PersonaExample{{User: "今天好累", Reply: "辛苦啦，\n歇一会儿吧。\n"}}
	if p.CharacterName != "苔灯" || p.Description != "苔灯是海边小镇的灯塔看守。\n她喜欢收集贝壳。\n" ||
		p.SpeakingStyle != "说话轻快， 句子短。" || !reflect.DeepEqual(p.Examples, want) {
		t.Errorf("template = %+v", p)
	}

	cfg = mustLoad(t, writeConfig(t, string(base)+strings.Replace(textPersonaYAML, "NAME", strings.Repeat("名", 21), 1)))
	err = cfg.ValidateServe()
	if want := "persona_templates.lighthouse.character_name must be at most 20 characters, got 21"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ValidateServe = %v, want an error containing %q", err, want)
	}
}

func TestLoad_ConfigWithoutProactiveUsesDefaults(t *testing.T) {
	path := writeConfig(t, `default_group_policy:
  reply:
    continuation_window: 60s
`)
	cfg := mustLoad(t, path)
	gp := cfg.DefaultGroupPolicy
	if gp.Reply.ContinuationWindow.Duration != time.Minute || gp.Reply.Debounce.Duration != 1500*time.Millisecond {
		t.Errorf("reply = %+v", gp.Reply)
	}
	if want := conloomapi.DefaultGroupPolicy().Proactive; gp.Proactive != want {
		t.Errorf("proactive = %+v, want defaults %+v", gp.Proactive, want)
	}

	path = writeConfig(t, `default_group_policy:
  proactive:
    enabled: false
    daily_limits: {high: 5}
`)
	gp = mustLoad(t, path).DefaultGroupPolicy
	want := conloomapi.DefaultGroupPolicy().Proactive
	want.Enabled = false
	want.DailyLimits.High = 5
	if gp.Proactive != want {
		t.Errorf("partial proactive = %+v, want %+v", gp.Proactive, want)
	}
}

func TestLoad_ConfigWithoutParticipationFieldsUsesDefaults(t *testing.T) {
	path := writeConfig(t, `default_group_policy:
  reply:
    command_prefix: /
    continuation_window: 60s
    debounce: 1500ms
    min_reply_interval: 2s
    user_rate_limit_per_minute: 6
  proactive:
    enabled: true
    default_level: low
    eligibility_ttl: 720h
    user_cooldown: 30m
    group_hourly_limit: 4
    daily_limits: {low: 2, mid: 4, high: 8}
`)
	cfg := mustLoad(t, path)
	gp := cfg.DefaultGroupPolicy
	want := conloomapi.DefaultGroupPolicy()
	want.Reply.ContinuationWindow = conloomapi.Duration{Duration: time.Minute}
	if !reflect.DeepEqual(gp, want) {
		t.Errorf("default_group_policy = %+v\nwant %+v", gp, want)
	}
	if err := gp.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	path = writeConfig(t, `default_group_policy:
  reply:
    turn_gap: 3s
    turn_cooldown: 2m
    quote: always
  proactive:
    join_cooldown: 1h
`)
	gp = mustLoad(t, path).DefaultGroupPolicy
	want = conloomapi.DefaultGroupPolicy()
	want.Reply.TurnGap = conloomapi.Duration{Duration: 3 * time.Second}
	want.Reply.TurnCooldown = conloomapi.Duration{Duration: 2 * time.Minute}
	want.Reply.Quote = conloomapi.QuoteAlways
	want.Proactive.JoinCooldown = conloomapi.Duration{Duration: time.Hour}
	if !reflect.DeepEqual(gp, want) {
		t.Errorf("partial participation fields = %+v\nwant %+v", gp, want)
	}
}

func TestLoad_MissingEnvRecorded(t *testing.T) {
	path := writeConfig(t, `adapters:
  napcat:
    ws_url: ${CONLOOM_TEST_UNSET_WS_URL}
    access_token: ${CONLOOM_TEST_UNSET_TOKEN:-}
    core_url: ${CONLOOM_TEST_UNSET_CORE:-http://127.0.0.1:8787}
`)
	cfg := mustLoad(t, path)
	if !slices.Equal(cfg.MissingEnv, []string{"CONLOOM_TEST_UNSET_WS_URL"}) {
		t.Errorf("MissingEnv = %v", cfg.MissingEnv)
	}
	if cfg.Adapters.NapCat.WSURL != "" || cfg.Adapters.NapCat.CoreURL != "http://127.0.0.1:8787" {
		t.Errorf("napcat = %+v", cfg.Adapters.NapCat)
	}
}

func TestLoad_PrivatePolicyFallsBackToGroup(t *testing.T) {
	cfg := mustLoad(t, writeConfig(t, `default_group_policy:
  context:
    max_recent_messages: 80
`))
	if cfg.DefaultPrivatePolicy != nil {
		t.Fatalf("DefaultPrivatePolicy = %+v, want nil without the section", cfg.DefaultPrivatePolicy)
	}
	if got := cfg.PrivatePolicy(); !reflect.DeepEqual(got, cfg.DefaultGroupPolicy) || got.Context.MaxRecentMessages != 80 {
		t.Fatalf("PrivatePolicy() = %+v, want the group defaults", got)
	}
}

func TestLoad_PrivatePolicyMissingKeysUseBuiltInDefaults(t *testing.T) {
	cfg := mustLoad(t, writeConfig(t, `default_group_policy:
  context:
    max_recent_messages: 80
default_private_policy:
  context:
    max_recent_messages: 200
  memory:
    enabled: true
    consolidate_every: 40
    consolidate_interval: 1h
    consolidate_min: 6
`))
	if cfg.DefaultPrivatePolicy == nil {
		t.Fatal("DefaultPrivatePolicy = nil, want the section")
	}
	want := conloomapi.DefaultGroupPolicy()
	want.Context.MaxRecentMessages = 200
	want.Memory.Enabled, want.Memory.ConsolidateEvery, want.Memory.ConsolidateMin = true, 40, 6
	want.Memory.ConsolidateInterval = conloomapi.Duration{Duration: time.Hour}
	if got := cfg.PrivatePolicy(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PrivatePolicy() = %+v\nwant %+v", got, want)
	}
	if cfg.DefaultGroupPolicy.Context.MaxRecentMessages != 80 || cfg.DefaultGroupPolicy.Memory.Enabled {
		t.Fatalf("group defaults changed: %+v", cfg.DefaultGroupPolicy)
	}
	if err := cfg.DefaultPrivatePolicy.Validate(); err != nil {
		t.Fatalf("private defaults invalid: %v", err)
	}
}

func TestLoad_PrivatePolicyUnknownFieldRejected(t *testing.T) {
	_, err := Load(writeConfig(t, "default_private_policy:\n  memroy:\n    enabled: true\n"))
	if err == nil || !strings.Contains(err.Error(), "memroy") {
		t.Fatalf("Load err = %v, want the unknown field named", err)
	}
}

func TestLoad_ExampleConfigPrivatePolicy(t *testing.T) {
	for k, v := range exampleEnv {
		t.Setenv(k, v)
	}
	cfg := mustLoad(t, "../../conloom.example.yaml")
	if cfg.DefaultPrivatePolicy == nil {
		t.Fatal("example has no default_private_policy")
	}
	pp := *cfg.DefaultPrivatePolicy
	m := pp.Memory
	if !m.Enabled || pp.Context.MaxRecentMessages != 200 || m.ConsolidateEvery != 40 ||
		m.ConsolidateInterval.Duration != time.Hour || m.ConsolidateMin != 6 {
		t.Errorf("default_private_policy = %+v", pp)
	}
	if cfg.DefaultGroupPolicy.Memory.ConsolidateMin != 20 {
		t.Errorf("default_group_policy.memory.consolidate_min = %d, want 20", cfg.DefaultGroupPolicy.Memory.ConsolidateMin)
	}
}

func TestLoad_UnknownFieldRejected(t *testing.T) {
	_, err := Load("testdata/unknown_field.yaml")
	if err == nil {
		t.Fatal("Load succeeded, want error for unknown field")
	}
	if !strings.Contains(err.Error(), "nmae") {
		t.Errorf("error %q does not name the unknown field", err)
	}
}

func TestValidateServe_WindowOrdering(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	p := cfg.ModelProfiles["short_context"]
	p.Context.SoftInputBudget = p.Context.ConfiguredWindow + 1
	cfg.ModelProfiles["short_context"] = p
	err := cfg.ValidateServe()
	if err == nil || !strings.Contains(err.Error(), "model_profiles.short_context.context") {
		t.Fatalf("soft > configured: err = %v", err)
	}

	p.Context.SoftInputBudget = 1000
	p.Context.ConfiguredWindow = p.Context.NativeWindow + 1
	cfg.ModelProfiles["short_context"] = p
	err = cfg.ValidateServe()
	if err == nil || !strings.Contains(err.Error(), "model_profiles.short_context.context") {
		t.Fatalf("configured > native: err = %v", err)
	}
}

func TestValidateServe_Rules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
		want   []string
	}{
		{"no bots", func(c *Config) { c.Bots = nil }, []string{"bots"}},
		{"duplicate bot id", func(c *Config) { c.Bots = append(c.Bots, c.Bots[0]) }, []string{"bots[1].id", "duplicate"}},
		{"bad private_chat", func(c *Config) { c.Bots[0].PrivateChat = "everyone" }, []string{"bots[0].private_chat"}},
		{"unknown persona template", func(c *Config) { c.Bots[0].PersonaTemplate = "nope" }, []string{"bots[0].persona_template", "nope"}},
		{"empty platform_self_id", func(c *Config) {
			c.Bots[0].PlatformSelfID = ""
			c.MissingEnv = []string{"CONLOOM_BOT_SELF_ID"}
		}, []string{"bots[0].platform_self_id", "CONLOOM_BOT_SELF_ID"}},
		{"no short_context", func(c *Config) { delete(c.ModelProfiles, "short_context") }, []string{"model_profiles.short_context"}},
		{"unsupported provider", func(c *Config) {
			p := c.ModelProfiles["long_context"]
			p.Provider = "openai"
			c.ModelProfiles["long_context"] = p
		}, []string{"model_profiles.long_context.provider", `"openai"`}},
		{"empty endpoint and model", func(c *Config) {
			p := c.ModelProfiles["long_context"]
			p.Endpoint, p.Model = "", ""
			c.ModelProfiles["long_context"] = p
		}, []string{"model_profiles.long_context.endpoint", "model_profiles.long_context.model"}},
		{"zero window", func(c *Config) {
			p := c.ModelProfiles["long_context"]
			p.Context.SoftInputBudget = 0
			c.ModelProfiles["long_context"] = p
		}, []string{"model_profiles.long_context.context.soft_input_budget"}},
		{"token for unknown bot", func(c *Config) { c.APITokens[0].BotInstanceID = "ghost" }, []string{"api_tokens[0].bot_instance_id", "ghost"}},
		{"bad scope", func(c *Config) { c.APITokens[0].Scopes = append(c.APITokens[0].Scopes, "root") }, []string{"api_tokens[0].scopes", "root"}},
		{"empty token", func(c *Config) { c.APITokens[0].Token = "" }, []string{"api_tokens[0].token"}},
		{"invalid persona template", func(c *Config) {
			p := c.PersonaTemplates["default"]
			p.Style.Warmth = 2
			c.PersonaTemplates["default"] = p
		}, []string{"persona_templates.default.style.warmth"}},
		{"invalid group policy", func(c *Config) {
			c.DefaultGroupPolicy.Concurrency.MaxInteractiveRequests = 3
			c.DefaultGroupPolicy.Review.Action = "explode"
		}, []string{"default_group_policy.concurrency.max_interactive_requests", "default_group_policy.review.action"}},
		{"invalid private policy", func(c *Config) {
			p := conloomapi.DefaultGroupPolicy()
			p.Memory.ConsolidateMin = 0
			c.DefaultPrivatePolicy = &p
		}, []string{"default_private_policy.memory.consolidate_min"}},
		{"bad reply quote", func(c *Config) { c.DefaultGroupPolicy.Reply.Quote = "sometimes" },
			[]string{"default_group_policy.reply.quote", `"sometimes"`}},
		{"empty platform", func(c *Config) { c.Bots[0].Platform = " " }, []string{"bots[0].platform:"}},
		{"empty group id", func(c *Config) {
			c.Bots[0].EnabledConversations[0].PlatformID = ""
			c.MissingEnv = []string{"CONLOOM_GROUP_ID"}
		}, []string{"bots[0].enabled_conversations[0].platform_id", "CONLOOM_GROUP_ID"}},
		{"bad conversation type", func(c *Config) { c.Bots[0].EnabledConversations[0].Type = "grop" },
			[]string{"bots[0].enabled_conversations[0].type", `"grop"`}},
		{"empty admin", func(c *Config) {
			c.Bots[0].Admins = []string{"29999", ""}
			c.MissingEnv = []string{"CONLOOM_ADMIN_QQ"}
		}, []string{"bots[0].admins[1]", "CONLOOM_ADMIN_QQ"}},
		{"zero max_concurrency", func(c *Config) {
			p := c.ModelProfiles["short_context"]
			p.Runtime.MaxConcurrency = 0
			c.ModelProfiles["short_context"] = p
		}, []string{"model_profiles.short_context.runtime.max_concurrency"}},
		{"zero timeout", func(c *Config) {
			p := c.ModelProfiles["short_context"]
			p.Runtime.Timeout.Duration = 0
			c.ModelProfiles["short_context"] = p
		}, []string{"model_profiles.short_context.runtime.timeout"}},
		{"negative timeout", func(c *Config) {
			p := c.ModelProfiles["short_context"]
			p.Runtime.Timeout.Duration = -time.Second
			c.ModelProfiles["short_context"] = p
		}, []string{"model_profiles.short_context.runtime.timeout"}},
		{"too many retries", func(c *Config) {
			p := c.ModelProfiles["long_context"]
			p.Runtime.MaxRetries = 6
			c.ModelProfiles["long_context"] = p
		}, []string{"model_profiles.long_context.runtime.max_retries"}},
		{"negative retries", func(c *Config) {
			p := c.ModelProfiles["long_context"]
			p.Runtime.MaxRetries = -1
			c.ModelProfiles["long_context"] = p
		}, []string{"model_profiles.long_context.runtime.max_retries"}},
		{"negative token factor", func(c *Config) {
			p := c.ModelProfiles["long_context"]
			p.TokenEstimateFactor = -0.5
			c.ModelProfiles["long_context"] = p
		}, []string{"model_profiles.long_context.token_estimate_factor"}},
		{"unknown log level", func(c *Config) { c.Observability.LogLevel = "verbose" }, []string{"observability.log_level", `"verbose"`}},
		{"unknown log format", func(c *Config) { c.Observability.LogFormat = "xml" }, []string{"observability.log_format", `"xml"`}},
		{"unknown timezone", func(c *Config) { c.Server.Timezone = "Asia/Shanghia" }, []string{"server.timezone", "Asia/Shanghia"}},
		{"duplicate token value", func(c *Config) {
			c.APITokens = append(c.APITokens, APIToken{Name: "second", Token: c.APITokens[0].Token, BotInstanceID: "bot_main"})
		}, []string{"api_tokens[1].token", "duplicate"}},
		{"duplicate token name", func(c *Config) {
			c.APITokens = append(c.APITokens, APIToken{Name: c.APITokens[0].Name, Token: "fake-other-token", BotInstanceID: "bot_main"})
		}, []string{"api_tokens[1].name", "duplicate"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustLoad(t, "testdata/valid.yaml")
			tc.mutate(cfg)
			err := cfg.ValidateServe()
			if err == nil {
				t.Fatal("ValidateServe succeeded, want error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestValidateNapCat_ReportsMissingEnv(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Adapters.NapCat.WSURL = ""
	cfg.MissingEnv = []string{"CONLOOM_NAPCAT_WS_URL"}
	err := cfg.ValidateNapCat()
	if err == nil {
		t.Fatal("ValidateNapCat succeeded, want error")
	}
	for _, w := range []string{"adapters.napcat.ws_url", "CONLOOM_NAPCAT_WS_URL"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}

	cfg.Adapters.NapCat.WSURL = "ws://127.0.0.1:9"
	cfg.Adapters.NapCat.CoreToken = ""
	cfg.Adapters.NapCat.BotInstanceID = ""
	err = cfg.ValidateNapCat()
	if err == nil {
		t.Fatal("ValidateNapCat succeeded, want error")
	}
	for _, w := range []string{"adapters.napcat.core_token", "adapters.napcat.bot_instance_id"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
}

func TestValidateMock(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	if err := cfg.ValidateMock(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	cfg.Adapters.Mock.CoreURL = ""
	cfg.MissingEnv = []string{"CONLOOM_MOCK_CORE_URL"}
	err := cfg.ValidateMock()
	if err == nil {
		t.Fatal("ValidateMock succeeded, want error")
	}
	for _, w := range []string{"adapters.mock.core_url", "CONLOOM_MOCK_CORE_URL"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
}

func TestValidateServe_PrivateChatValues(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	for _, v := range []string{"admins", "allowlist", "all", "off"} {
		cfg.Bots[0].PrivateChat = v
		if err := cfg.ValidateServe(); err != nil {
			t.Errorf("private_chat %q: %v", v, err)
		}
	}
	cfg.Bots[0].PrivateChat = "everyone"
	err := cfg.ValidateServe()
	if err == nil || !strings.Contains(err.Error(), "admins|allowlist|all|off") {
		t.Errorf("private_chat everyone: err = %v, want the accepted values listed", err)
	}
}

func TestBotConfig_UnusedPrivateConversations(t *testing.T) {
	b := BotConfig{EnabledConversations: []conloomapi.ConversationRef{
		{Type: conloomapi.ConvGroup, PlatformID: "10001"},
		{Type: conloomapi.ConvPrivate, PlatformID: "20001"},
		{Type: conloomapi.ConvPrivate, PlatformID: "20002"},
	}}
	for _, mode := range []string{"admins", "all", "off"} {
		b.PrivateChat = mode
		if got := b.UnusedPrivateConversations(); !slices.Equal(got, []string{"20001", "20002"}) {
			t.Errorf("private_chat %s: unused = %v, want [20001 20002]", mode, got)
		}
	}
	b.PrivateChat = "allowlist"
	if got := b.UnusedPrivateConversations(); len(got) != 0 {
		t.Errorf("private_chat allowlist: unused = %v, want none", got)
	}
	b.PrivateChat = "admins"
	b.EnabledConversations = b.EnabledConversations[:1]
	if got := b.UnusedPrivateConversations(); len(got) != 0 {
		t.Errorf("no private entries: unused = %v, want none", got)
	}
}

func TestValidateServe_AcceptsBoundaryValues(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	p := cfg.ModelProfiles["short_context"]
	p.Runtime.MaxConcurrency = 1
	p.Runtime.MaxRetries = 0
	p.TokenEstimateFactor = 0
	cfg.ModelProfiles["short_context"] = p
	p = cfg.ModelProfiles["long_context"]
	p.Runtime.MaxRetries = 5
	cfg.ModelProfiles["long_context"] = p
	cfg.Bots[0].Admins = nil
	cfg.Bots[0].EnabledConversations = append(cfg.Bots[0].EnabledConversations,
		conloomapi.ConversationRef{Type: "private", PlatformID: "20001"})
	cfg.Server.Timezone = "UTC"
	for _, o := range []ObservabilityConfig{
		{LogLevel: "", LogFormat: ""},
		{LogLevel: "debug", LogFormat: "text"},
		{LogLevel: "WARNING", LogFormat: "JSON"},
		{LogLevel: "warn"},
		{LogLevel: "error"},
	} {
		cfg.Observability = o
		if err := cfg.ValidateServe(); err != nil {
			t.Errorf("observability %+v: %v", o, err)
		}
	}
}

func TestValidateAdapters_URLsAndLogging(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(c *Config)
		validate func(c *Config) error
		want     []string
	}{
		{"napcat core_url without scheme", func(c *Config) { c.Adapters.NapCat.CoreURL = "127.0.0.1:8787" },
			(*Config).ValidateNapCat, []string{"adapters.napcat.core_url", "http"}},
		{"napcat core_url ws scheme", func(c *Config) { c.Adapters.NapCat.CoreURL = "ws://127.0.0.1:8787" },
			(*Config).ValidateNapCat, []string{"adapters.napcat.core_url"}},
		{"napcat ws_url http scheme", func(c *Config) { c.Adapters.NapCat.WSURL = "http://127.0.0.1:3001" },
			(*Config).ValidateNapCat, []string{"adapters.napcat.ws_url", "ws"}},
		{"napcat ws_url without host", func(c *Config) { c.Adapters.NapCat.WSURL = "ws://" },
			(*Config).ValidateNapCat, []string{"adapters.napcat.ws_url"}},
		{"napcat log level", func(c *Config) { c.Observability.LogLevel = "verbose" },
			(*Config).ValidateNapCat, []string{"observability.log_level"}},
		{"mock core_url scheme", func(c *Config) { c.Adapters.Mock.CoreURL = "ftp://127.0.0.1:8787" },
			(*Config).ValidateMock, []string{"adapters.mock.core_url"}},
		{"mock log format", func(c *Config) { c.Observability.LogFormat = "xml" },
			(*Config).ValidateMock, []string{"observability.log_format"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustLoad(t, "testdata/valid.yaml")
			tc.mutate(cfg)
			err := tc.validate(cfg)
			if err == nil {
				t.Fatal("validation succeeded, want error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}

	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Adapters.NapCat.WSURL = "wss://napcat.example:3001/path"
	cfg.Adapters.NapCat.CoreURL = "https://127.0.0.1:8787"
	cfg.Adapters.Mock.CoreURL = "HTTP://127.0.0.1:8787/"
	if err := cfg.ValidateNapCat(); err != nil {
		t.Errorf("ValidateNapCat: %v", err)
	}
	if err := cfg.ValidateMock(); err != nil {
		t.Errorf("ValidateMock: %v", err)
	}
}

func TestValidateServe_IgnoresAdapterFields(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Adapters = AdaptersConfig{}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe should not require adapter fields: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	cfg := &Config{
		APITokens: []APIToken{{Token: "tok-a"}, {Token: ""}, {Token: "tok-b"}},
		ModelProfiles: map[string]ModelProfile{
			"short_context": {APIKey: "key-short"},
			"long_context":  {APIKey: ""},
		},
		Adapters: AdaptersConfig{
			NapCat: NapCatConfig{AccessToken: "napcat-access", CoreToken: "tok-a"},
			Mock:   MockConfig{CoreToken: "mock-core"},
		},
	}
	got := cfg.Secrets()
	slices.Sort(got)
	want := []string{"key-short", "mock-core", "napcat-access", "tok-a", "tok-b"}
	if !slices.Equal(got, want) {
		t.Errorf("Secrets() = %v, want %v", got, want)
	}
}

func TestBot(t *testing.T) {
	cfg := &Config{Bots: []BotConfig{{ID: "bot_a"}, {ID: "bot_b", Name: "B"}}}
	b, ok := cfg.Bot("bot_b")
	if !ok || b.Name != "B" {
		t.Errorf("Bot(bot_b) = %+v, %v", b, ok)
	}
	if _, ok := cfg.Bot("bot_c"); ok {
		t.Error("Bot(bot_c) found, want missing")
	}
}

func TestBotConfig_PrivateAllowed(t *testing.T) {
	bot := BotConfig{Admins: []string{"29999"}, EnabledConversations: []conloomapi.ConversationRef{
		{Type: conloomapi.ConvPrivate, PlatformID: "20001"}, {Type: conloomapi.ConvGroup, PlatformID: "20002"}}}
	for mode, want := range map[string]map[string]bool{
		"off":       {"29999": false, "20001": false, "20002": false},
		"admins":    {"29999": true, "20001": false, "20002": false},
		"allowlist": {"29999": true, "20001": true, "20002": false},
		"all":       {"29999": true, "20001": true, "20002": true},
	} {
		bot.PrivateChat = mode
		for user, w := range want {
			if got := bot.PrivateAllowed(user); got != w {
				t.Errorf("%s: PrivateAllowed(%s) = %v, want %v", mode, user, got, w)
			}
		}
	}
}
