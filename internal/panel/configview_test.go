package panel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// secretConfig fills h.cfg with every secret field set to SECRET-<field>, plus
// entries whose secrets are empty.
func secretConfig(h *overviewHarness) {
	c := h.cfg
	c.Server = config.ServerConfig{Listen: "127.0.0.1:8787", Timezone: "Asia/Shanghai", MetricsEnabled: true}
	c.Storage.DataDir = "/var/lib/conloom"
	c.APITokens = []config.APIToken{
		{Name: "qq-adapter", Token: "SECRET-token", BotInstanceID: "qq-main", Scopes: []string{"events", "outbox"}},
		{Name: "tg-adapter", Token: "", BotInstanceID: "tg-main"},
	}
	c.Bots[0].Admins = []string{"20001"}
	c.Bots[0].EnabledConversations = []conloomapi.ConversationRef{{Type: conloomapi.ConvGroup, PlatformID: "10001"}}
	c.ModelProfiles = map[string]config.ModelProfile{
		"short_context": {Provider: config.ProviderOpenAICompatible, Endpoint: "http://example.invalid/v1", Model: "qwen3",
			APIKey: "SECRET-api_key", ExtraBody: map[string]any{"enable_thinking": false},
			Runtime: config.ProfileRuntime{MaxConcurrency: 2, Timeout: dur(time.Minute)}},
		"long_context": {Provider: config.ProviderOpenAICompatible, Endpoint: "http://example.invalid/v2", Model: "deepseek"},
	}
	c.PersonaTemplates = map[string]conloomapi.PersonaDoc{"default": conloomapi.DefaultPersona()}
	c.DefaultGroupPolicy = conloomapi.DefaultGroupPolicy()
	c.Runtime = config.RuntimeConfig{ActionTTL: dur(10 * time.Minute), AuditRetention: dur(2160 * time.Hour)}
	c.Embedding = &config.EmbeddingConfig{Endpoint: "http://example.invalid:11434/v1", Model: "bge-m3",
		APIKey: "SECRET-embedding_api_key", BatchSize: 16, MinScore: 0.48}
	c.Panel.Users = append(c.Panel.Users, config.PanelUser{Name: "viewer", PasswordHash: "SECRET-password_hash"})
	c.Adapters = config.AdaptersConfig{
		NapCat: config.NapCatConfig{WSURL: "ws://example.invalid:3001", AccessToken: "SECRET-access_token",
			CoreURL: "http://example.invalid:8787", CoreToken: "SECRET-napcat_core_token", BotInstanceID: "qq-main"},
		Telegram: config.TelegramConfig{Token: "SECRET-telegram_token", APIBase: "https://example.invalid",
			CoreToken: "SECRET-telegram_core_token", BotInstanceID: "tg-main"},
		Mock: config.MockConfig{CoreURL: "http://example.invalid:8787", CoreToken: ""},
	}
	c.Observability = config.ObservabilityConfig{LogLevel: "info", LogFormat: "json"}
}

// jsonAt follows path (map keys and slice indexes) into the decoded JSON v.
func jsonAt(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%v: %v is not an object", path, v)
			}
			if v, ok = m[k]; !ok {
				t.Fatalf("%v: no key %q in %v", path, k, m)
			}
		case int:
			s, ok := v.([]any)
			if !ok || k >= len(s) {
				t.Fatalf("%v: no index %d in %v", path, k, v)
			}
			v = s[k]
		}
	}
	return v
}

func TestConfigView_NoSecrets(t *testing.T) {
	h := newOverviewHarness(t, nil)
	secretConfig(h)

	w := h.get("/panel/api/config")
	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, "SECRET-") {
		t.Errorf("config view leaks a secret: %s", body)
	}
	for _, s := range h.cfg.Secrets() {
		if strings.Contains(body, s) {
			t.Errorf("config view contains secret %q", s)
		}
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	cfg := resp.Config
	for _, tc := range []struct {
		path []any
		want any
	}{
		{[]any{"model_profiles", "short_context", "api_key"}, "(已设置)"},
		{[]any{"model_profiles", "long_context", "api_key"}, "(未设置)"},
		{[]any{"embedding", "api_key"}, "(已设置)"},
		{[]any{"panel", "users", 0, "password_hash"}, "(已设置)"},
		{[]any{"panel", "users", 1, "password_hash"}, "(已设置)"},
		{[]any{"adapters", "napcat", "access_token"}, "(已设置)"},
		{[]any{"adapters", "napcat", "core_token"}, "(已设置)"},
		{[]any{"adapters", "telegram", "token"}, "(已设置)"},
		{[]any{"adapters", "telegram", "core_token"}, "(已设置)"},
		{[]any{"adapters", "mock", "core_token"}, "(未设置)"},

		{[]any{"model_profiles", "short_context", "endpoint"}, "http://example.invalid/v1"},
		{[]any{"model_profiles", "short_context", "runtime", "timeout"}, "1m0s"},
		{[]any{"model_profiles", "short_context", "extra_body", "enable_thinking"}, false},
		{[]any{"embedding", "endpoint"}, "http://example.invalid:11434/v1"},
		{[]any{"adapters", "napcat", "ws_url"}, "ws://example.invalid:3001"},
		{[]any{"adapters", "telegram", "bot_instance_id"}, "tg-main"},
		{[]any{"server", "listen"}, "127.0.0.1:8787"},
		{[]any{"storage", "data_dir"}, "/var/lib/conloom"},
		{[]any{"bots", 0, "id"}, "qq-main"},
		{[]any{"bots", 0, "admins", 0}, "20001"},
		{[]any{"bots", 0, "enabled_conversations", 0, "platform_id"}, "10001"},
		{[]any{"panel", "users", 1, "name"}, "viewer"},
		{[]any{"panel", "session_ttl"}, "168h0m0s"},
		{[]any{"runtime", "action_ttl"}, "10m0s"},
		{[]any{"runtime", "usage_retention"}, "9600h0m0s"},
		{[]any{"default_group_policy", "memory", "enabled"}, false},
		{[]any{"observability", "log_level"}, "info"},
	} {
		if got := jsonAt(t, cfg, tc.path...); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v = %#v, want %#v", tc.path, got, tc.want)
		}
	}

	wantTokens := []any{
		map[string]any{"name": "qq-adapter", "bot_instance_id": "qq-main", "scopes": []any{"events", "outbox"}},
		map[string]any{"name": "tg-adapter", "bot_instance_id": "tg-main", "scopes": []any{}},
	}
	if got := cfg["api_tokens"]; !reflect.DeepEqual(got, wantTokens) {
		t.Errorf("api_tokens = %#v, want %#v", got, wantTokens)
	}

	h.cfg.Embedding = nil
	h.cfg.DefaultPrivatePolicy = nil
	h.getJSON("/panel/api/config", &resp)
	if v, ok := resp.Config["embedding"]; !ok || v != nil {
		t.Errorf("embedding without a section = %v (present %v), want null", v, ok)
	}
	if v, ok := resp.Config["default_private_policy"]; !ok || v != nil {
		t.Errorf("default_private_policy unset = %v (present %v), want null", v, ok)
	}
}
