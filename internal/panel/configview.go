package panel

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/kinako404/conloom/internal/botdir"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// What a secret field shows instead of its value, and what a URL shows
// instead of a query value or fragment.
const (
	secretSet   = "(已设置)"
	secretUnset = "(未设置)"
	urlHidden   = "(已隐藏)"
)

func (s *Server) configView(w http.ResponseWriter, r *http.Request, _ *Session) {
	s.writeJSON(w, http.StatusOK, map[string]any{"config": redactedConfig(s.d.Config, s.d.Bots, s.d.Policies.Defaults)})
}

// redactedConfig is the configuration in effect, keyed as in the YAML file,
// with the bots of bots (see botsView). It is built field by field from an
// explicit list, so a field added to the configuration is left out until it
// is listed here; secrets only show whether they are set, and URLs go
// through redactURL.
//
// The default policies are not the config file's: they are the ones in the
// database that take effect, as defaults returns them, each shown as
// {"source": "panel", "version", "policy"} since the settings page edits
// them. Their policy is shown whole: it is behaviour settings shared with the
// Bearer API's policy endpoints and holds no credentials.
// TestConfigView_PassThroughTypesHaveNoSecretFields fails if its type gains a
// field that looks like one. The persona templates of an old config file are
// not shown: they no longer take effect (see
// config.Config.LegacyPersonaTemplates).
func redactedConfig(c *config.Config, bots *botdir.Directory, defaults func(kind string) store.PolicyDefaults) map[string]any {
	return map[string]any{
		"server": map[string]any{
			"listen": c.Server.Listen, "timezone": c.Server.Timezone, "metrics_enabled": c.Server.MetricsEnabled,
		},
		"storage":                map[string]any{"data_dir": c.Storage.DataDir},
		"api_tokens":             apiTokensView(c.APITokens),
		"bots":                   botsView(bots),
		"model_profiles":         modelProfilesView(c.ModelProfiles),
		"default_group_policy":   defaultPolicyView(defaults(policy.KindGroup)),
		"default_private_policy": defaultPolicyView(defaults(policy.KindPrivate)),
		"runtime":                runtimeView(c.Runtime),
		"skills":                 map[string]any{"dir": c.Skills.Dir},
		"embedding":              embeddingView(c.Embedding),
		"panel":                  panelView(c.Panel),
		"adapters":               adaptersView(c.Adapters),
		"observability": map[string]any{
			"log_level": c.Observability.LogLevel, "log_format": c.Observability.LogFormat,
			"log_message_content": c.Observability.LogMessageContent,
		},
	}
}

func defaultPolicyView(d store.PolicyDefaults) map[string]any {
	return map[string]any{"source": sourcePanel, "version": d.Version, "policy": d.Policy}
}

func secret(v string) string {
	if v == "" {
		return secretUnset
	}
	return secretSet
}

// redactURL shows a configured URL without what may hold a credential: the
// user info is removed, each query value and the fragment are replaced by
// urlHidden (query keys are kept). A URL that does not parse, or parses as
// opaque (scheme:rest without //, where rest may be user:password@host), only
// shows that it is set.
func redactURL(v string) string {
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Opaque != "" {
		return secretSet
	}
	u.User = nil
	if u.RawQuery != "" {
		params := strings.Split(u.RawQuery, "&")
		for i, p := range params {
			if key, _, ok := strings.Cut(p, "="); ok {
				params[i] = key + "=" + urlHidden
			}
		}
		u.RawQuery = strings.Join(params, "&")
	}
	hasFragment := u.Fragment != "" || u.RawFragment != ""
	u.Fragment, u.RawFragment = "", ""
	s := u.String()
	if hasFragment {
		s += "#" + urlHidden
	}
	return s
}

// strs is v, empty rather than nil.
func strs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func apiTokensView(tokens []config.APIToken) []map[string]any {
	out := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, map[string]any{"name": t.Name, "bot_instance_id": t.BotInstanceID, "scopes": strs(t.Scopes)})
	}
	return out
}

// botsView shows the bots of bots keyed as in the YAML file: the config
// file's as written in it, which is only their identity (their settings are
// in the database and shown with the bots), then those created in the panel
// as they take effect, each marked with "source": "panel".
func botsView(bots *botdir.Directory) []map[string]any {
	configured, all := bots.ConfigBots(), bots.All()
	out := make([]map[string]any, 0, len(all))
	for _, b := range configured {
		out = append(out, map[string]any{"id": b.ID, "platform": b.Platform, "platform_self_id": b.PlatformSelfID})
	}
	for _, b := range all[len(configured):] {
		convs := make([]map[string]any, 0, len(b.EnabledConversations))
		for _, c := range b.EnabledConversations {
			convs = append(convs, map[string]any{"type": c.Type, "platform_id": c.PlatformID})
		}
		out = append(out, map[string]any{
			"id": b.ID, "platform": b.Platform, "name": b.Name, "platform_self_id": b.PlatformSelfID,
			"admins": strs(b.Admins), "enabled_conversations": convs, "private_chat": b.PrivateChat,
			"persona_template": b.PrivateTemplate, "skills": strs(b.Skills), "source": sourcePanel,
		})
	}
	return out
}

func modelProfilesView(profiles map[string]config.ModelProfile) map[string]any {
	out := make(map[string]any, len(profiles))
	for name, p := range profiles {
		out[name] = map[string]any{
			"provider": p.Provider, "endpoint": redactURL(p.Endpoint), "model": p.Model, "api_key": secret(p.APIKey),
			"modalities": strs(p.Modalities),
			"context": map[string]any{
				"native_window": p.Context.NativeWindow, "configured_window": p.Context.ConfiguredWindow,
				"soft_input_budget": p.Context.SoftInputBudget, "reserved_output_tokens": p.Context.ReservedOutputTokens,
				"safety_margin_tokens": p.Context.SafetyMarginTokens,
			},
			"runtime": map[string]any{
				"max_concurrency": p.Runtime.MaxConcurrency, "gpu_kv_cache_tokens": p.Runtime.GPUKVCacheTokens,
				"cpu_offloaded_kv_tokens": p.Runtime.CPUOffloadedKVTokens, "prefix_caching": p.Runtime.PrefixCaching,
				"timeout": p.Runtime.Timeout, "max_retries": p.Runtime.MaxRetries,
			},
			"privacy": map[string]any{
				"data_location": p.Privacy.DataLocation, "allow_sensitive_content": p.Privacy.AllowSensitiveContent,
			},
			"extra_body":            extraBodyView(p.ExtraBody),
			"token_estimate_factor": p.TokenEstimateFactor,
		}
	}
	return out
}

// extraBodyView shows the keys of a free-form extra_body. Booleans and numbers
// are shown as they are; strings, lists, objects and nulls only show whether
// they are set, since any of them may hold a credential.
func extraBodyView(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		switch v := v.(type) {
		case bool, int, int64, uint64, float64:
			out[k] = v
		case string:
			out[k] = secret(v)
		case nil:
			out[k] = secretUnset
		default:
			out[k] = secretSet
		}
	}
	return out
}

func runtimeView(rt config.RuntimeConfig) map[string]any {
	usageRetention := rt.UsageRetention
	if usageRetention.Duration <= 0 {
		usageRetention = conloomapi.Duration{Duration: config.DefaultUsageRetention}
	}
	return map[string]any{
		"action_ttl": rt.ActionTTL, "outbox_lease": rt.OutboxLease, "stale_task_after": rt.StaleTaskAfter,
		"idempotency_retention": rt.IdempotencyRetention, "max_merge_window": rt.MaxMergeWindow,
		"session_idle": rt.SessionIdle, "ops_retention": rt.OpsRetention, "audit_retention": rt.AuditRetention,
		"usage_retention": usageRetention,
	}
}

// embeddingView is nil, shown as null, when there is no embedding section.
func embeddingView(e *config.EmbeddingConfig) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"endpoint": redactURL(e.Endpoint), "model": e.Model, "api_key": secret(e.APIKey), "timeout": e.Timeout,
		"batch_size": e.BatchSize, "min_score": e.MinScore,
	}
}

func panelView(p *config.PanelConfig) map[string]any {
	if p == nil {
		return nil
	}
	users := make([]map[string]any, 0, len(p.Users))
	for _, u := range p.Users {
		users = append(users, map[string]any{"name": u.Name, "password_hash": secret(u.PasswordHash)})
	}
	return map[string]any{
		"users": users, "session_ttl": p.SessionTTL, "idle_timeout": p.IdleTimeout, "cookie_secure": p.CookieSecure,
		"listen": p.Listen,
	}
}

func adaptersView(a config.AdaptersConfig) map[string]any {
	return map[string]any{
		"napcat": map[string]any{
			"ws_url": redactURL(a.NapCat.WSURL), "access_token": secret(a.NapCat.AccessToken), "core_url": redactURL(a.NapCat.CoreURL),
			"core_token": secret(a.NapCat.CoreToken), "bot_instance_id": a.NapCat.BotInstanceID,
			"state_dir": a.NapCat.StateDir,
		},
		"telegram": map[string]any{
			"token": secret(a.Telegram.Token), "api_base": redactURL(a.Telegram.APIBase), "poll_timeout": a.Telegram.PollTimeout,
			"core_url": redactURL(a.Telegram.CoreURL), "core_token": secret(a.Telegram.CoreToken),
			"bot_instance_id": a.Telegram.BotInstanceID, "state_dir": a.Telegram.StateDir,
		},
		"mock": map[string]any{
			"core_url": redactURL(a.Mock.CoreURL), "core_token": secret(a.Mock.CoreToken), "bot_instance_id": a.Mock.BotInstanceID,
		},
	}
}
