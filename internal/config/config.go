// Package config loads and validates the ConLoom YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

type Config struct {
	Server             ServerConfig                     `yaml:"server"`
	Storage            StorageConfig                    `yaml:"storage"`
	APITokens          []APIToken                       `yaml:"api_tokens"`
	Bots               []BotConfig                      `yaml:"bots"`
	ModelProfiles      map[string]ModelProfile          `yaml:"model_profiles"`
	PersonaTemplates   map[string]conloomapi.PersonaDoc `yaml:"persona_templates"`
	DefaultGroupPolicy conloomapi.GroupPolicy           `yaml:"default_group_policy"`
	// DefaultPrivatePolicy is the default policy of private conversations; nil
	// when the file has no default_private_policy (see PrivatePolicy).
	DefaultPrivatePolicy *conloomapi.GroupPolicy `yaml:"default_private_policy"`
	Runtime              RuntimeConfig           `yaml:"runtime"`
	Skills               SkillsConfig            `yaml:"skills"`
	// Embedding configures the text embedding service of semantic search; nil
	// when the file has no embedding section, which turns semantic search off.
	Embedding *EmbeddingConfig `yaml:"embedding"`
	// Panel configures the web admin panel; nil when the file has no panel
	// section, which turns the panel off.
	Panel         *PanelConfig        `yaml:"panel"`
	Adapters      AdaptersConfig      `yaml:"adapters"`
	Observability ObservabilityConfig `yaml:"observability"`

	// Path is the absolute path of the loaded file.
	Path string `yaml:"-"`
	// MissingEnv lists referenced environment variables that were unset and had no default.
	MissingEnv []string `yaml:"-"`
}

type ServerConfig struct {
	Listen         string `yaml:"listen"`
	Timezone       string `yaml:"timezone"`
	MetricsEnabled bool   `yaml:"metrics_enabled"`
}

type StorageConfig struct {
	DataDir string `yaml:"data_dir"`
}

// SkillsConfig locates the skill files that add to or replace the built-in
// skills. An empty Dir uses only the built-in skills; a relative Dir is
// resolved against the configuration file's directory.
type SkillsConfig struct {
	Dir string `yaml:"dir"`
}

// EmbeddingConfig locates an OpenAI-compatible embeddings endpoint: requests go
// to Endpoint + "/embeddings". Timeout bounds each request and BatchSize the
// texts sent in one request. MinScore is the cosine similarity below which a
// chunk is not a semantic hit; it depends on the model.
type EmbeddingConfig struct {
	Endpoint  string              `yaml:"endpoint"`
	Model     string              `yaml:"model"`
	APIKey    string              `yaml:"api_key"`
	Timeout   conloomapi.Duration `yaml:"timeout"`
	BatchSize int                 `yaml:"batch_size"`
	MinScore  float64             `yaml:"min_score"`
}

// DefaultEmbeddingMinScore is the default of embedding.min_score, calibrated
// on bge-m3: related chunks scored about 0.50–0.63, most unrelated pairs
// below 0.46. Another model needs calibrating again.
const DefaultEmbeddingMinScore = 0.48

func defaultEmbedding() EmbeddingConfig {
	return EmbeddingConfig{Model: "bge-m3", Timeout: dur(10 * time.Second), BatchSize: 16, MinScore: DefaultEmbeddingMinScore}
}

// PanelUser is a web panel login; PasswordHash is a pbkdf2-sha256 hash string.
type PanelUser struct {
	Name         string `yaml:"name"`
	PasswordHash string `yaml:"password_hash"`
}

// PanelConfig configures the web admin panel. A session ends SessionTTL after
// login or IdleTimeout after its last request, whichever comes first.
// CookieSecure sets the Secure flag on the session cookie.
type PanelConfig struct {
	Users        []PanelUser         `yaml:"users"`
	SessionTTL   conloomapi.Duration `yaml:"session_ttl"`
	IdleTimeout  conloomapi.Duration `yaml:"idle_timeout"`
	CookieSecure bool                `yaml:"cookie_secure"`
}

// Defaults of panel.session_ttl and panel.idle_timeout.
const (
	DefaultPanelSessionTTL  = 168 * time.Hour
	DefaultPanelIdleTimeout = 12 * time.Hour
)

func defaultPanel() PanelConfig {
	return PanelConfig{SessionTTL: dur(DefaultPanelSessionTTL), IdleTimeout: dur(DefaultPanelIdleTimeout)}
}

type APIToken struct {
	Name          string   `yaml:"name"`
	Token         string   `yaml:"token"`
	BotInstanceID string   `yaml:"bot_instance_id"`
	Scopes        []string `yaml:"scopes"`
}

type BotConfig struct {
	ID                   string                       `yaml:"id"`
	Platform             string                       `yaml:"platform"`
	Name                 string                       `yaml:"name"`
	PlatformSelfID       string                       `yaml:"platform_self_id"`
	Admins               []string                     `yaml:"admins"`
	EnabledConversations []conloomapi.ConversationRef `yaml:"enabled_conversations"`
	PrivateChat          string                       `yaml:"private_chat"`
	PersonaTemplate      string                       `yaml:"persona_template"`
	// Skills names the skills the bot may use; none when empty.
	Skills []string `yaml:"skills"`
}

// UnusedPrivateConversations returns the platform IDs of the private entries in
// EnabledConversations, which only take effect when PrivateChat is "allowlist".
func (b BotConfig) UnusedPrivateConversations() []string {
	if b.PrivateChat == "allowlist" {
		return nil
	}
	var ids []string
	for _, c := range b.EnabledConversations {
		if c.Type == conloomapi.ConvPrivate {
			ids = append(ids, c.PlatformID)
		}
	}
	return ids
}

// PrivateAllowed reports whether the user may chat with the bot privately
// under PrivateChat: never for "off", only admins for "admins", admins and the
// private entries of EnabledConversations for "allowlist", anyone for "all".
func (b BotConfig) PrivateAllowed(platformUserID string) bool {
	admin := slices.Contains(b.Admins, platformUserID)
	switch b.PrivateChat {
	case "off":
		return false
	case "admins":
		return admin
	case "allowlist":
		return admin || slices.ContainsFunc(b.EnabledConversations, func(c conloomapi.ConversationRef) bool {
			return c.Type == conloomapi.ConvPrivate && c.PlatformID == platformUserID
		})
	}
	return true
}

// ProviderOpenAICompatible is the only supported ModelProfile.Provider and the default.
const ProviderOpenAICompatible = "openai_compatible"

type ModelProfile struct {
	Provider            string         `yaml:"provider"`
	Endpoint            string         `yaml:"endpoint"`
	Model               string         `yaml:"model"`
	APIKey              string         `yaml:"api_key"`
	Modalities          []string       `yaml:"modalities"`
	Context             ContextLimits  `yaml:"context"`
	Runtime             ProfileRuntime `yaml:"runtime"`
	Privacy             Privacy        `yaml:"privacy"`
	ExtraBody           map[string]any `yaml:"extra_body"`
	TokenEstimateFactor float64        `yaml:"token_estimate_factor"`
}

type ContextLimits struct {
	NativeWindow         int `yaml:"native_window"`
	ConfiguredWindow     int `yaml:"configured_window"`
	SoftInputBudget      int `yaml:"soft_input_budget"`
	ReservedOutputTokens int `yaml:"reserved_output_tokens"`
	SafetyMarginTokens   int `yaml:"safety_margin_tokens"`
}

type ProfileRuntime struct {
	MaxConcurrency       int                 `yaml:"max_concurrency"`
	GPUKVCacheTokens     int                 `yaml:"gpu_kv_cache_tokens"`
	CPUOffloadedKVTokens int                 `yaml:"cpu_offloaded_kv_tokens"`
	PrefixCaching        bool                `yaml:"prefix_caching"`
	Timeout              conloomapi.Duration `yaml:"timeout"`
	MaxRetries           int                 `yaml:"max_retries"`
}

type Privacy struct {
	DataLocation          string `yaml:"data_location"`
	AllowSensitiveContent bool   `yaml:"allow_sensitive_content"`
}

type RuntimeConfig struct {
	ActionTTL            conloomapi.Duration `yaml:"action_ttl"`
	OutboxLease          conloomapi.Duration `yaml:"outbox_lease"`
	StaleTaskAfter       conloomapi.Duration `yaml:"stale_task_after"`
	IdempotencyRetention conloomapi.Duration `yaml:"idempotency_retention"`
	MaxMergeWindow       conloomapi.Duration `yaml:"max_merge_window"`
	SessionIdle          conloomapi.Duration `yaml:"session_idle"`
	// OpsRetention is how long finished tasks and their acked or expired
	// outbox actions are kept; never shorter than IdempotencyRetention, whose
	// stored responses name the tasks.
	OpsRetention conloomapi.Duration `yaml:"ops_retention"`
	// AuditRetention is how long audit events are kept.
	AuditRetention conloomapi.Duration `yaml:"audit_retention"`
	// UsageRetention is how long model call records are kept; 0 when omitted,
	// which means DefaultUsageRetention.
	UsageRetention conloomapi.Duration `yaml:"usage_retention"`
}

type AdaptersConfig struct {
	NapCat   NapCatConfig   `yaml:"napcat"`
	Telegram TelegramConfig `yaml:"telegram"`
	Mock     MockConfig     `yaml:"mock"`
}

// TelegramConfig configures the Telegram Bot API adapter. PollTimeout is the
// getUpdates long-poll time, in whole seconds.
type TelegramConfig struct {
	Token         string              `yaml:"token"`
	APIBase       string              `yaml:"api_base"`
	PollTimeout   conloomapi.Duration `yaml:"poll_timeout"`
	CoreURL       string              `yaml:"core_url"`
	CoreToken     string              `yaml:"core_token"`
	BotInstanceID string              `yaml:"bot_instance_id"`
	StateDir      string              `yaml:"state_dir"`
}

type NapCatConfig struct {
	WSURL         string `yaml:"ws_url"`
	AccessToken   string `yaml:"access_token"`
	CoreURL       string `yaml:"core_url"`
	CoreToken     string `yaml:"core_token"`
	BotInstanceID string `yaml:"bot_instance_id"`
	StateDir      string `yaml:"state_dir"`
}

type MockConfig struct {
	CoreURL       string `yaml:"core_url"`
	CoreToken     string `yaml:"core_token"`
	BotInstanceID string `yaml:"bot_instance_id"`
}

type ObservabilityConfig struct {
	LogLevel          string `yaml:"log_level"`
	LogFormat         string `yaml:"log_format"`
	LogMessageContent bool   `yaml:"log_message_content"`
}

// Defaults and allowed ranges of runtime.ops_retention and runtime.audit_retention.
const (
	DefaultOpsRetention   = 720 * time.Hour
	DefaultAuditRetention = 2160 * time.Hour
	MinOpsRetention       = 24 * time.Hour
	MaxOpsRetention       = 8760 * time.Hour
	MinAuditRetention     = 24 * time.Hour
	MaxAuditRetention     = 87600 * time.Hour
)

// DefaultUsageRetention is the default of runtime.usage_retention.
const DefaultUsageRetention = 400 * 24 * time.Hour

func dur(d time.Duration) conloomapi.Duration { return conloomapi.Duration{Duration: d} }

func defaultConfig() *Config {
	return &Config{
		Server:             ServerConfig{Listen: "127.0.0.1:8787", Timezone: "Asia/Shanghai", MetricsEnabled: true},
		DefaultGroupPolicy: conloomapi.DefaultGroupPolicy(),
		Runtime: RuntimeConfig{
			ActionTTL:            dur(10 * time.Minute),
			OutboxLease:          dur(60 * time.Second),
			StaleTaskAfter:       dur(2 * time.Minute),
			IdempotencyRetention: dur(168 * time.Hour),
			MaxMergeWindow:       dur(4500 * time.Millisecond),
			SessionIdle:          dur(5 * time.Minute),
			OpsRetention:         dur(DefaultOpsRetention),
			AuditRetention:       dur(DefaultAuditRetention),
		},
		Adapters: AdaptersConfig{
			Telegram: TelegramConfig{APIBase: "https://api.telegram.org", PollTimeout: dur(50 * time.Second)},
		},
		Observability: ObservabilityConfig{LogLevel: "info", LogFormat: "json"},
	}
}

func defaultBot() BotConfig {
	return BotConfig{Name: "织语", PrivateChat: "admins", PersonaTemplate: "default"}
}

func defaultModelProfile() ModelProfile {
	return ModelProfile{
		Runtime:             ProfileRuntime{MaxConcurrency: 2, Timeout: dur(60 * time.Second), MaxRetries: 2},
		TokenEstimateFactor: 1.0,
	}
}

// Load reads the YAML file at path. Variables from a .env file next to it are
// available for expansion but never override the process environment. Unknown
// keys are rejected; unset fields get their defaults; relative directories are
// resolved against the file's directory.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	dir := filepath.Dir(abs)
	dotenv, err := readDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		return nil, fmt.Errorf("config: .env: %w", err)
	}
	lookup := func(k string) (string, bool) {
		if v, ok := os.LookupEnv(k); ok {
			return v, true
		}
		v, ok := dotenv[k]
		return v, ok
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("config %s: %w", abs, err)
	}
	cfg := defaultConfig()
	cfg.Path = abs
	cfg.MissingEnv = expandNode(&root, lookup)

	if root.Kind != 0 {
		expanded, err := yaml.Marshal(&root)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", abs, err)
		}
		if n := mappingValue(&root, "default_private_policy"); n != nil && n.Tag != "!!null" {
			p := conloomapi.DefaultGroupPolicy()
			cfg.DefaultPrivatePolicy = &p
		}
		if n := mappingValue(&root, "embedding"); n != nil && n.Tag != "!!null" {
			e := defaultEmbedding()
			cfg.Embedding = &e
		}
		if n := mappingValue(&root, "panel"); n != nil && n.Tag != "!!null" {
			p := defaultPanel()
			cfg.Panel = &p
		}
		dec := yaml.NewDecoder(bytes.NewReader(expanded))
		dec.KnownFields(true)
		if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config %s: %w", abs, err)
		}
		if err := applyElementDefaults(&root, cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", abs, err)
		}
	}

	if cfg.PersonaTemplates == nil {
		cfg.PersonaTemplates = map[string]conloomapi.PersonaDoc{}
	}
	if _, ok := cfg.PersonaTemplates["default"]; !ok {
		cfg.PersonaTemplates["default"] = conloomapi.DefaultPersona()
	}
	for name, p := range cfg.ModelProfiles {
		if p.Provider == "" {
			p.Provider = ProviderOpenAICompatible
			cfg.ModelProfiles[name] = p
		}
	}
	if cfg.Storage.DataDir == "" {
		cfg.Storage.DataDir = "./data"
	}
	cfg.Storage.DataDir = resolveDir(dir, cfg.Storage.DataDir)
	if cfg.Skills.Dir != "" {
		cfg.Skills.Dir = resolveDir(dir, cfg.Skills.Dir)
	}
	for _, stateDir := range []*string{&cfg.Adapters.NapCat.StateDir, &cfg.Adapters.Telegram.StateDir} {
		if *stateDir == "" {
			*stateDir = cfg.Storage.DataDir
		} else {
			*stateDir = resolveDir(dir, *stateDir)
		}
	}
	return cfg, nil
}

// applyElementDefaults re-decodes each bot and model profile on top of its
// defaults, so omitted fields get defaults while explicit zero values are kept.
// The strict decode has already rejected unknown keys.
func applyElementDefaults(root *yaml.Node, cfg *Config) error {
	if bots := mappingValue(root, "bots"); bots != nil && bots.Kind == yaml.SequenceNode {
		for i, n := range bots.Content {
			if i >= len(cfg.Bots) {
				break
			}
			b := defaultBot()
			if err := n.Decode(&b); err != nil {
				return err
			}
			cfg.Bots[i] = b
		}
	}
	if profiles := mappingValue(root, "model_profiles"); profiles != nil && profiles.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(profiles.Content); i += 2 {
			name := profiles.Content[i].Value
			if _, ok := cfg.ModelProfiles[name]; !ok {
				continue
			}
			p := defaultModelProfile()
			if err := profiles.Content[i+1].Decode(&p); err != nil {
				return err
			}
			cfg.ModelProfiles[name] = p
		}
	}
	return nil
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			v := n.Content[i+1]
			if v.Kind == yaml.AliasNode {
				v = v.Alias
			}
			return v
		}
	}
	return nil
}

func resolveDir(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// PrivatePolicy is the default policy of private conversations:
// DefaultPrivatePolicy when the file sets one, else DefaultGroupPolicy.
func (c *Config) PrivatePolicy() conloomapi.GroupPolicy {
	if c.DefaultPrivatePolicy != nil {
		return *c.DefaultPrivatePolicy
	}
	return c.DefaultGroupPolicy
}

// Bot returns the bot with the given ID.
func (c *Config) Bot(id string) (BotConfig, bool) {
	for _, b := range c.Bots {
		if b.ID == id {
			return b, true
		}
	}
	return BotConfig{}, false
}

// DBPath is the SQLite database file inside the data directory.
func (c *Config) DBPath() string {
	return filepath.Join(c.Storage.DataDir, "conloom.db")
}

// Secrets returns every non-empty credential in the config (model and embedding
// API keys, API tokens, adapter access, bot and core tokens, panel password
// hashes), without duplicates.
func (c *Config) Secrets() []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, t := range c.APITokens {
		add(t.Token)
	}
	for _, name := range slices.Sorted(maps.Keys(c.ModelProfiles)) {
		add(c.ModelProfiles[name].APIKey)
	}
	add(c.Adapters.NapCat.AccessToken)
	add(c.Adapters.NapCat.CoreToken)
	add(c.Adapters.Telegram.Token)
	add(c.Adapters.Telegram.CoreToken)
	add(c.Adapters.Mock.CoreToken)
	if c.Embedding != nil {
		add(c.Embedding.APIKey)
	}
	if c.Panel != nil {
		for _, u := range c.Panel.Users {
			add(u.PasswordHash)
		}
	}
	return out
}
