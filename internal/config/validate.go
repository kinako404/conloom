package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kinako404/conloom/pkg/conloomapi"
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
		p.required(path+".platform_self_id", b.PlatformSelfID)
		if !slices.Contains([]string{"admins", "all", "off"}, b.PrivateChat) {
			p.addf(path+".private_chat", "must be one of admins|all|off, got %q", b.PrivateChat)
		}
		if _, ok := c.PersonaTemplates[b.PersonaTemplate]; !ok {
			p.addf(path+".persona_template", "unknown persona template %q", b.PersonaTemplate)
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
	}

	for i, t := range c.APITokens {
		path := fmt.Sprintf("api_tokens[%d]", i)
		p.required(path+".token", t.Token)
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

	return p.err(c, "serve")
}

// ValidateNapCat checks the fields the NapCat adapter needs.
func (c *Config) ValidateNapCat() error {
	var p problems
	n := c.Adapters.NapCat
	p.required("adapters.napcat.ws_url", n.WSURL)
	p.required("adapters.napcat.core_url", n.CoreURL)
	p.required("adapters.napcat.core_token", n.CoreToken)
	p.required("adapters.napcat.bot_instance_id", n.BotInstanceID)
	return p.err(c, "adapter napcat")
}

// ValidateMock checks the fields the mock adapter needs.
func (c *Config) ValidateMock() error {
	var p problems
	m := c.Adapters.Mock
	p.required("adapters.mock.core_url", m.CoreURL)
	p.required("adapters.mock.core_token", m.CoreToken)
	p.required("adapters.mock.bot_instance_id", m.BotInstanceID)
	return p.err(c, "mock")
}
