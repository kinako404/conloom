package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// maxModelRetries bounds model_profiles.*.runtime.max_retries; the retry
// backoff doubles per attempt, so larger values stall a conversation.
const maxModelRetries = 5

// maxEmbeddingTimeout and maxEmbeddingBatch bound the embedding section.
const (
	maxEmbeddingTimeout = 5 * time.Minute
	maxEmbeddingBatch   = 256
)

var allScopes = []string{
	conloomapi.ScopeEventsWrite,
	conloomapi.ScopeTasksRead,
	conloomapi.ScopeOutboxRead,
	conloomapi.ScopeOutboxAck,
	conloomapi.ScopeMemoryRead,
	conloomapi.ScopeMemoryWrite,
	conloomapi.ScopePersonaRead,
	conloomapi.ScopePolicyWrite,
	conloomapi.ScopeAdminRead,
}

type problems struct {
	list     []string
	hasEmpty bool
}

func (p *problems) addf(path, format string, args ...any) {
	p.list = append(p.list, path+": "+fmt.Sprintf(format, args...))
}

func (p *problems) required(path, value string) {
	if strings.TrimSpace(value) == "" {
		p.addf(path, "must not be empty")
		p.hasEmpty = true
	}
}

// nested records each rule violation in err under the prefix path.
func (p *problems) nested(prefix string, err error) {
	if err == nil {
		return
	}
	errs := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	}
	for _, e := range errs {
		p.list = append(p.list, prefix+"."+e.Error())
	}
}

// absURL requires value, when set, to be an absolute URL with one of schemes and a host.
func (p *problems) absURL(path, value string, schemes ...string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	u, err := url.Parse(value)
	if err != nil || !slices.Contains(schemes, u.Scheme) || u.Host == "" {
		p.addf(path, "must be a %s URL, got %q", strings.Join(schemes, "|"), value)
	}
}

func (p *problems) observability(o ObservabilityConfig) {
	if o.LogLevel != "" && !slices.Contains([]string{"debug", "info", "warn", "warning", "error"}, strings.ToLower(o.LogLevel)) {
		p.addf("observability.log_level", "must be one of debug|info|warn|error, got %q", o.LogLevel)
	}
	if o.LogFormat != "" && !slices.Contains([]string{"json", "text"}, strings.ToLower(o.LogFormat)) {
		p.addf("observability.log_format", "must be one of json|text, got %q", o.LogFormat)
	}
}

// retention requires d to be within [lo, hi].
func (p *problems) retention(path string, d, lo, hi time.Duration) {
	if d < lo || d > hi {
		p.addf(path, "must be in [%s, %s], got %s", hours(lo), hours(hi), d)
	}
}

// hours formats a whole number of hours as "<n>h".
func hours(d time.Duration) string {
	return fmt.Sprintf("%dh", int64(d/time.Hour))
}

func (p *problems) err(c *Config, command string) error {
	if len(p.list) == 0 {
		return nil
	}
	if p.hasEmpty && len(c.MissingEnv) > 0 {
		p.list = append(p.list, "unset environment variables referenced by the config: "+strings.Join(c.MissingEnv, ", "))
	}
	return errors.New("invalid config for " + command + ":\n  " + strings.Join(p.list, "\n  "))
}

// ValidateServe checks the fields the core server needs.
func (c *Config) ValidateServe() error {
	var p problems

	p.required("server.listen", c.Server.Listen)
	p.listenAddr("server.listen", c.Server.Listen)
	if tz := c.Server.Timezone; tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			p.addf("server.timezone", "unknown time zone %q", tz)
		}
	}
	p.observability(c.Observability)
	p.retention("runtime.ops_retention", c.Runtime.OpsRetention.Duration, MinOpsRetention, MaxOpsRetention)
	p.retention("runtime.audit_retention", c.Runtime.AuditRetention.Duration, MinAuditRetention, MaxAuditRetention)

	if len(c.Bots) == 0 {
		p.addf("bots", "at least one bot is required")
	}
	botIDs := map[string]bool{}
	for i, b := range c.Bots {
		path := fmt.Sprintf("bots[%d]", i)
		p.required(path+".id", b.ID)
		if b.ID != "" && botIDs[b.ID] {
			p.addf(path+".id", "duplicate bot id %q", b.ID)
		}
		botIDs[b.ID] = true
		p.required(path+".platform", b.Platform)
		p.required(path+".platform_self_id", b.PlatformSelfID)
		for j, a := range b.Admins {
			p.required(fmt.Sprintf("%s.admins[%d]", path, j), a)
		}
		for j, conv := range b.EnabledConversations {
			cpath := fmt.Sprintf("%s.enabled_conversations[%d]", path, j)
			if conv.Type != conloomapi.ConvGroup && conv.Type != conloomapi.ConvPrivate {
				p.addf(cpath+".type", "must be one of group|private, got %q", conv.Type)
			}
			p.required(cpath+".platform_id", conv.PlatformID)
		}
		if !slices.Contains([]string{"admins", "allowlist", "all", "off"}, b.PrivateChat) {
			p.addf(path+".private_chat", "must be one of admins|allowlist|all|off, got %q", b.PrivateChat)
		}
		if _, ok := c.PersonaTemplates[b.PersonaTemplate]; !ok {
			p.addf(path+".persona_template", "unknown persona template %q", b.PersonaTemplate)
		}
		for j, s := range b.Skills {
			spath := fmt.Sprintf("%s.skills[%d]", path, j)
			if strings.TrimSpace(s) == "" {
				p.addf(spath, "must not be empty")
			} else if slices.Contains(b.Skills[:j], s) {
				p.addf(spath, "duplicate skill %q", s)
			}
		}
	}

	if _, ok := c.ModelProfiles["short_context"]; !ok {
		p.addf("model_profiles.short_context", "is required")
	}
	for _, name := range slices.Sorted(maps.Keys(c.ModelProfiles)) {
		mp := c.ModelProfiles[name]
		path := "model_profiles." + name
		if mp.Provider != ProviderOpenAICompatible {
			p.addf(path+".provider", "must be %s, got %q", ProviderOpenAICompatible, mp.Provider)
		}
		p.required(path+".endpoint", mp.Endpoint)
		p.required(path+".model", mp.Model)
		ctx := mp.Context
		for _, w := range []struct {
			key string
			v   int
		}{
			{"native_window", ctx.NativeWindow},
			{"configured_window", ctx.ConfiguredWindow},
			{"soft_input_budget", ctx.SoftInputBudget},
		} {
			if w.v <= 0 {
				p.addf(path+".context."+w.key, "must be > 0, got %d", w.v)
			}
		}
		if ctx.SoftInputBudget > ctx.ConfiguredWindow {
			p.addf(path+".context", "soft_input_budget (%d) must be <= configured_window (%d)", ctx.SoftInputBudget, ctx.ConfiguredWindow)
		}
		if ctx.ConfiguredWindow > ctx.NativeWindow {
			p.addf(path+".context", "configured_window (%d) must be <= native_window (%d)", ctx.ConfiguredWindow, ctx.NativeWindow)
		}
		rt := mp.Runtime
		if rt.MaxConcurrency < 1 {
			p.addf(path+".runtime.max_concurrency", "must be >= 1, got %d", rt.MaxConcurrency)
		}
		if rt.Timeout.Duration <= 0 {
			p.addf(path+".runtime.timeout", "must be > 0, got %s", rt.Timeout.Duration)
		}
		if rt.MaxRetries < 0 || rt.MaxRetries > maxModelRetries {
			p.addf(path+".runtime.max_retries", "must be between 0 and %d, got %d", maxModelRetries, rt.MaxRetries)
		}
		if mp.TokenEstimateFactor < 0 {
			p.addf(path+".token_estimate_factor", "must be >= 0, got %v", mp.TokenEstimateFactor)
		}
	}

	tokenValues, tokenNames := map[string]bool{}, map[string]bool{}
	for i, t := range c.APITokens {
		path := fmt.Sprintf("api_tokens[%d]", i)
		p.required(path+".token", t.Token)
		if t.Token != "" && tokenValues[t.Token] {
			p.addf(path+".token", "duplicate token value (each token must be unique)")
		}
		tokenValues[t.Token] = true
		if t.Name != "" && tokenNames[t.Name] {
			p.addf(path+".name", "duplicate token name %q", t.Name)
		}
		tokenNames[t.Name] = true
		if !botIDs[t.BotInstanceID] {
			p.addf(path+".bot_instance_id", "unknown bot %q", t.BotInstanceID)
		}
		for _, s := range t.Scopes {
			if !slices.Contains(allScopes, s) {
				p.addf(path+".scopes", "unknown scope %q", s)
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(c.PersonaTemplates)) {
		p.nested("persona_templates."+name, c.PersonaTemplates[name].Validate())
	}
	p.nested("default_group_policy", c.DefaultGroupPolicy.Validate())
	if c.DefaultPrivatePolicy != nil {
		p.nested("default_private_policy", c.DefaultPrivatePolicy.Validate())
	}
	if e := c.Embedding; e != nil {
		p.required("embedding.endpoint", e.Endpoint)
		p.absURL("embedding.endpoint", e.Endpoint, "http", "https")
		p.required("embedding.model", e.Model)
		if d := e.Timeout.Duration; d <= 0 || d > maxEmbeddingTimeout {
			p.addf("embedding.timeout", "must be > 0 and <= %s, got %s", maxEmbeddingTimeout, d)
		}
		if !(e.MinScore >= 0 && e.MinScore <= 1) {
			p.addf("embedding.min_score", "must be in [0, 1], got %v", e.MinScore)
		}
		if e.BatchSize < 1 || e.BatchSize > maxEmbeddingBatch {
			p.addf("embedding.batch_size", "must be between 1 and %d, got %d", maxEmbeddingBatch, e.BatchSize)
		}
	}
	if c.Panel != nil {
		p.panel(c.Panel, c.Server.Listen)
	}

	return p.err(c, "serve")
}

// Panel password hashes have the form pbkdf2-sha256:<iterations>:<salt>:<hash>
// with a 16-byte salt and a 32-byte key in unpadded base64url. internal/panel
// owns the format but imports this package, so the format is checked here too.
const (
	panelHashScheme        = "pbkdf2-sha256"
	minPanelHashIterations = 100000
	panelHashSaltLen       = 16
	panelHashKeyLen        = 32
)

// listenAddr requires value, when set, to be a host:port with a numeric port;
// the host may be empty to listen on every interface.
func (p *problems) listenAddr(path, value string) {
	if value == "" {
		return
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		p.addf(path, "must be host:port, got %q", value)
		return
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		p.addf(path, "port must be a number in [0, 65535], got %q", value)
	}
}

// sameListenAddr reports whether listening on both a and b would claim the
// same port: equal non-zero ports on the same host, or on any host when one
// of them is unspecified. Port 0 picks a free port, so it never conflicts.
func sameListenAddr(a, b string) bool {
	ah, ap, aerr := net.SplitHostPort(a)
	bh, bp, berr := net.SplitHostPort(b)
	if aerr != nil || berr != nil || ap != bp || ap == "0" {
		return false
	}
	return strings.EqualFold(ah, bh) || unspecifiedHost(ah) || unspecifiedHost(bh)
}

func unspecifiedHost(h string) bool {
	if h == "" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsUnspecified()
}

// panel checks the panel section. Problems never quote a password hash.
func (p *problems) panel(pc *PanelConfig, serverListen string) {
	p.listenAddr("panel.listen", pc.Listen)
	if pc.Listen != "" && sameListenAddr(pc.Listen, serverListen) {
		p.addf("panel.listen", "must differ from server.listen (%q), got %q", serverListen, pc.Listen)
	}
	if len(pc.Users) == 0 {
		p.addf("panel.users", "at least one user is required")
	}
	names := map[string]bool{}
	for i, u := range pc.Users {
		path := fmt.Sprintf("panel.users[%d]", i)
		p.required(path+".name", u.Name)
		if u.Name != "" && names[u.Name] {
			p.addf(path+".name", "duplicate user %q", u.Name)
		}
		names[u.Name] = true
		if strings.TrimSpace(u.PasswordHash) == "" {
			p.required(path+".password_hash", u.PasswordHash)
		} else if err := checkPanelHash(u.PasswordHash); err != nil {
			p.addf(path+".password_hash", "%v (generate one with `conloom panel hash-password`)", err)
		}
	}
	if d := pc.SessionTTL.Duration; d <= 0 {
		p.addf("panel.session_ttl", "must be > 0, got %s", d)
	}
	if d := pc.IdleTimeout.Duration; d <= 0 {
		p.addf("panel.idle_timeout", "must be > 0, got %s", d)
	}
}

func checkPanelHash(encoded string) error {
	parts := strings.Split(encoded, ":")
	if len(parts) != 4 || parts[0] != panelHashScheme {
		return errors.New("must have the form " + panelHashScheme + ":<iterations>:<salt>:<hash>")
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || parts[1] != strconv.Itoa(n) {
		return errors.New("iterations must be a decimal number")
	}
	if n < minPanelHashIterations {
		return fmt.Errorf("iterations must be >= %d, got %d", minPanelHashIterations, n)
	}
	b64 := base64.RawURLEncoding.Strict()
	if salt, err := b64.DecodeString(parts[2]); err != nil || len(salt) != panelHashSaltLen {
		return fmt.Errorf("salt must be %d bytes of unpadded base64url", panelHashSaltLen)
	}
	if key, err := b64.DecodeString(parts[3]); err != nil || len(key) != panelHashKeyLen {
		return fmt.Errorf("hash must be %d bytes of unpadded base64url", panelHashKeyLen)
	}
	return nil
}

// ValidateNapCat checks the fields the NapCat adapter needs.
func (c *Config) ValidateNapCat() error {
	var p problems
	n := c.Adapters.NapCat
	p.required("adapters.napcat.ws_url", n.WSURL)
	p.absURL("adapters.napcat.ws_url", n.WSURL, "ws", "wss")
	p.required("adapters.napcat.core_url", n.CoreURL)
	p.absURL("adapters.napcat.core_url", n.CoreURL, "http", "https")
	p.required("adapters.napcat.core_token", n.CoreToken)
	p.required("adapters.napcat.bot_instance_id", n.BotInstanceID)
	p.observability(c.Observability)
	return p.err(c, "adapter napcat")
}

// ValidateTelegram checks the fields the Telegram adapter needs. Problems
// never quote the bot token.
func (c *Config) ValidateTelegram() error {
	var p problems
	tg := c.Adapters.Telegram
	p.required("adapters.telegram.token", tg.Token)
	p.required("adapters.telegram.api_base", tg.APIBase)
	p.absURL("adapters.telegram.api_base", tg.APIBase, "http", "https")
	if d := tg.PollTimeout.Duration; d < time.Second || d > 60*time.Second {
		p.addf("adapters.telegram.poll_timeout", "must be between 1s and 60s, got %s", d)
	}
	p.required("adapters.telegram.core_url", tg.CoreURL)
	p.absURL("adapters.telegram.core_url", tg.CoreURL, "http", "https")
	p.required("adapters.telegram.core_token", tg.CoreToken)
	p.required("adapters.telegram.bot_instance_id", tg.BotInstanceID)
	p.observability(c.Observability)
	return p.err(c, "adapter telegram")
}

// ValidateMock checks the fields the mock adapter needs.
func (c *Config) ValidateMock() error {
	var p problems
	m := c.Adapters.Mock
	p.required("adapters.mock.core_url", m.CoreURL)
	p.absURL("adapters.mock.core_url", m.CoreURL, "http", "https")
	p.required("adapters.mock.core_token", m.CoreToken)
	p.required("adapters.mock.bot_instance_id", m.BotInstanceID)
	p.observability(c.Observability)
	return p.err(c, "mock")
}
