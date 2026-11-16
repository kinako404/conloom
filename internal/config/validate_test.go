package config

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

var (
	fakeSalt      = strings.Repeat("A", 22)
	fakeKey       = strings.Repeat("B", 42) + "A"
	fakePanelHash = "pbkdf2-sha256:600000:" + fakeSalt + ":" + fakeKey
)

func validPanel() *PanelConfig {
	p := defaultPanel()
	p.Users = []PanelUser{{Name: "admin", PasswordHash: fakePanelHash}}
	return &p
}

func TestValidate_Panel(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Panel = validPanel()
	cfg.Panel.Users = append(cfg.Panel.Users, PanelUser{Name: "ops", PasswordHash: "pbkdf2-sha256:100000:" + fakeSalt + ":" + fakeKey})
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("valid panel: %v", err)
	}

	badHashes := []string{
		"pbkdf2-sha256:99999:" + fakeSalt + ":" + fakeKey,
		"pbkdf2-sha256:600000:" + fakeSalt,
		"pbkdf2-sha256:600000:" + fakeSalt + ":" + fakeKey + ":extra",
		"pbkdf2-sha256:600000:" + strings.Repeat("*", 22) + ":" + fakeKey,
		"pbkdf2-sha256:600000:" + fakeSalt + ":" + fakeKey[:40],
		"pbkdf2-sha1:600000:" + fakeSalt + ":" + fakeKey,
		"$2y$10$abcdefghijklmnopqrstuv",
	}
	cases := []struct {
		name    string
		mutate  func(p *PanelConfig)
		want    []string
		missing []string
	}{
		{"no users", func(p *PanelConfig) { p.Users = nil }, []string{"panel.users", "at least one"}, nil},
		{"duplicate user", func(p *PanelConfig) { p.Users = append(p.Users, p.Users[0]) },
			[]string{"panel.users[1].name", `duplicate user "admin"`}, nil},
		{"empty name", func(p *PanelConfig) { p.Users[0].Name = " " }, []string{"panel.users[0].name"}, nil},
		// Login trims the name it is given, so such a user could never log in.
		{"leading space in name", func(p *PanelConfig) { p.Users[0].Name = " admin" },
			[]string{"panel.users[0].name", "leading or trailing white space"}, nil},
		{"trailing tab in name", func(p *PanelConfig) { p.Users[0].Name = "admin\t" },
			[]string{"panel.users[0].name", "leading or trailing white space"}, nil},
		{"empty hash", func(p *PanelConfig) { p.Users[0].PasswordHash = "" }, []string{"panel.users[0].password_hash"}, nil},
		{"zero session_ttl", func(p *PanelConfig) { p.SessionTTL.Duration = 0 }, []string{"panel.session_ttl", "must be > 0"}, nil},
		{"negative session_ttl", func(p *PanelConfig) { p.SessionTTL.Duration = -time.Hour }, []string{"panel.session_ttl"}, nil},
		{"zero idle_timeout", func(p *PanelConfig) { p.IdleTimeout.Duration = 0 }, []string{"panel.idle_timeout", "must be > 0"}, nil},
		{"negative idle_timeout", func(p *PanelConfig) { p.IdleTimeout.Duration = -time.Minute }, []string{"panel.idle_timeout"}, nil},
	}
	for i, h := range badHashes {
		cases = append(cases, struct {
			name    string
			mutate  func(p *PanelConfig)
			want    []string
			missing []string
		}{"bad hash " + string(rune('a'+i)), func(p *PanelConfig) { p.Users[0].PasswordHash = h },
			[]string{"panel.users[0].password_hash"}, []string{h, fakeSalt, fakeKey[:40]}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustLoad(t, "testdata/valid.yaml")
			cfg.Panel = validPanel()
			tc.mutate(cfg.Panel)
			err := cfg.ValidateServe()
			if err == nil {
				t.Fatal("ValidateServe succeeded, want error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
			for _, m := range tc.missing {
				if strings.Contains(err.Error(), m) {
					t.Errorf("error %q quotes the hash", err)
				}
			}
		})
	}
}

func TestValidateConfig_MinorTemplateAdult(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	adult := conloomapi.DefaultPersona()
	adult.Adult = true
	minor := conloomapi.DefaultPersona()
	minor.CharacterName, minor.Minor = "角色甲", true
	cfg.LegacyPersonaTemplates = map[string]conloomapi.PersonaDoc{"grownup": adult, "child": minor}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("adult template and minor template: %v", err)
	}
	minor.Adult = true
	cfg.LegacyPersonaTemplates["child"] = minor
	err := cfg.ValidateServe()
	if want := "persona_templates.child.adult: a minor persona cannot be adult"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want containing %q", err, want)
	}
	if err != nil && strings.Contains(err.Error(), "persona_templates.grownup") {
		t.Errorf("err = %v, names the adult template", err)
	}
}

func TestValidate_ConfigBotTemplateNotChecked(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	if cfg.LegacyPersonaTemplates != nil {
		t.Fatalf("persona_templates of a config without them = %+v, want none, not even default", cfg.LegacyPersonaTemplates)
	}
	cfg.Bots[0].Legacy.PersonaTemplate = ptr("night-owl")
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("bot naming a template that is not in the config file: %v", err)
	}
	cfg.LegacyPersonaTemplates = map[string]conloomapi.PersonaDoc{"lighthouse": conloomapi.DefaultPersona()}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("bot naming a template that is neither in the legacy templates: %v", err)
	}
}

func TestValidate_ConfigBotBlankTemplate(t *testing.T) {
	base, err := os.ReadFile("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Omitted, persona_template is not required: the template is a setting
	// in the database.
	if cfg := mustLoad(t, writeConfig(t, string(base))); cfg.Bots[0].Legacy.PersonaTemplate != nil || cfg.ValidateServe() != nil {
		t.Fatalf("bot without persona_template = %v, %v; want none, valid", cfg.Bots[0].Legacy.PersonaTemplate, cfg.ValidateServe())
	}
	for _, v := range []string{`""`, `"  "`} {
		yaml := strings.Replace(string(base), "    platform: qq\n", "    platform: qq\n    persona_template: "+v+"\n", 1)
		cfg := mustLoad(t, writeConfig(t, yaml))
		err := cfg.ValidateServe()
		if want := "bots[0].persona_template: must not be empty"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("persona_template: %s: ValidateServe = %v, want an error containing %q", v, err, want)
		}
	}
}

func TestValidate_PanelHashFromUnsetEnv(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Panel = validPanel()
	cfg.Panel.Users[0].PasswordHash = ""
	cfg.MissingEnv = []string{"CONLOOM_PANEL_ADMIN_HASH"}
	err := cfg.ValidateServe()
	if err == nil || !strings.Contains(err.Error(), "CONLOOM_PANEL_ADMIN_HASH") {
		t.Fatalf("err = %v, want it to name the unset variable", err)
	}
}

func TestValidate_NoPanelSection(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Panel = nil
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("config without panel: %v", err)
	}
}

func TestValidate_PanelListen(t *testing.T) {
	cases := []struct {
		server, panel string
		want          []string
	}{
		{"127.0.0.1:8787", "", nil},
		{"127.0.0.1:8787", "0.0.0.0:8788", nil},
		{"127.0.0.1:8787", "127.0.0.1:8788", nil},
		{"127.0.0.1:8787", "[::]:8788", nil},
		{"127.0.0.1:8787", ":8788", nil},
		{"127.0.0.1:8787", "192.0.2.1:8787", nil},
		{"127.0.0.1:0", "127.0.0.1:0", nil},
		{"127.0.0.1:8787", "127.0.0.1:8787", []string{"panel.listen", "must differ from server.listen"}},
		{"127.0.0.1:8787", "0.0.0.0:8787", []string{"panel.listen", "must differ from server.listen"}},
		{":8787", "127.0.0.1:8787", []string{"panel.listen", "must differ from server.listen"}},
		{"127.0.0.1:8787", "8788", []string{"panel.listen", "host:port"}},
		{"127.0.0.1:8787", "0.0.0.0:", []string{"panel.listen", "port"}},
		{"127.0.0.1:8787", "0.0.0.0:99999", []string{"panel.listen", "port"}},
		{"127.0.0.1:8787", "0.0.0.0:http", []string{"panel.listen", "port"}},
		{"127.0.0.1", "", []string{"server.listen", "host:port"}},
		{"", "", []string{"server.listen", "must not be empty"}},
	}
	for _, tc := range cases {
		t.Run(tc.server+" "+tc.panel, func(t *testing.T) {
			cfg := mustLoad(t, "testdata/valid.yaml")
			cfg.Server.Listen = tc.server
			cfg.Panel = validPanel()
			cfg.Panel.Listen = tc.panel
			err := cfg.ValidateServe()
			if tc.want == nil {
				if err != nil {
					t.Fatalf("ValidateServe: %v", err)
				}
				return
			}
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

func TestSecrets_IncludePanelPasswordHashes(t *testing.T) {
	other := "pbkdf2-sha256:100000:" + fakeSalt + ":" + strings.Repeat("C", 42) + "A"
	cfg := &Config{
		APITokens: []APIToken{{Token: "tok-a"}},
		Panel: &PanelConfig{Users: []PanelUser{
			{Name: "admin", PasswordHash: fakePanelHash},
			{Name: "ops", PasswordHash: other},
			{Name: "empty"},
		}},
	}
	got := cfg.Secrets()
	slices.Sort(got)
	want := []string{fakePanelHash, other, "tok-a"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("Secrets() = %v, want %v", got, want)
	}
}

func TestValidate_PanelPrefixReserved(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Bots[0].ID = "panel-a1b2c3d4"
	err := cfg.ValidateServe()
	if want := `bots[0].id: "panel-" is reserved for bots created in the panel`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want containing %q", err, want)
	}
	for _, id := range []string{"panelbot", "my-panel-bot", "Panel-main"} {
		cfg := mustLoad(t, "testdata/valid.yaml")
		cfg.Bots[0].ID = id
		for i := range cfg.APITokens {
			cfg.APITokens[i].BotInstanceID = id
		}
		if err := cfg.ValidateServe(); err != nil {
			t.Errorf("bot id %q: %v", id, err)
		}
	}
}

func TestValidate_BotIDAndPlatformWithoutColon(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Bots[0].ID = "qq:main"
	for i := range cfg.APITokens {
		cfg.APITokens[i].BotInstanceID = "qq:main"
	}
	cfg.Bots[0].Platform = "qq:napcat"
	err := cfg.ValidateServe()
	for _, want := range []string{`bots[0].id: must not contain ":"`, `bots[0].platform: must not contain ":"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want containing %q", err, want)
		}
	}
	if !ValidBotID("qq-main") || !ValidBotID("panel-k3x9q2ma") {
		t.Error("ValidBotID refused an ID without a colon")
	}
	for _, id := range []string{"", "qq:main", ":", "panel-a:b"} {
		if ValidBotID(id) {
			t.Errorf("ValidBotID(%q) = true, want false", id)
		}
	}
}

func TestValidate_ModelLimitsAtTheirBounds(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	p := cfg.ModelProfiles["short_context"]
	p.Context.ReservedOutputTokens, p.Context.SafetyMarginTokens = 16384, 16383
	p.Runtime.GPUKVCacheTokens = p.Context.ConfiguredWindow
	cfg.ModelProfiles["short_context"] = p
	cfg.DefaultGroupPolicy.Context.MaxContextTokens = 32768
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("limits at their bounds: %v", err)
	}
	if got := cfg.ReservedContextTokens(); got != 32767 {
		t.Errorf("ReservedContextTokens() = %d, want short_context's reserved output plus safety margin 32767", got)
	}
	delete(cfg.ModelProfiles, "short_context")
	if got := cfg.ReservedContextTokens(); got != 0 {
		t.Errorf("ReservedContextTokens() without short_context = %d, want 0", got)
	}
}

func TestValidate_VisionNeedsImageModality(t *testing.T) {
	withVision := func(modalities ...string) *Config {
		cfg := mustLoad(t, "testdata/valid.yaml")
		p := cfg.ModelProfiles["short_context"]
		p.Model, p.Modalities = "fake-vision", modalities
		cfg.ModelProfiles["vision"] = p
		return cfg
	}
	if err := withVision("text", "image").ValidateServe(); err != nil {
		t.Fatalf("vision with image modality: %v", err)
	}
	for _, modalities := range [][]string{nil, {"text"}} {
		err := withVision(modalities...).ValidateServe()
		if want := "model_profiles.vision.modalities: must include image"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("modalities %v: err = %v, want containing %q", modalities, err, want)
		}
	}
	cfg := withVision("image")
	p := cfg.ModelProfiles["vision"]
	p.Context.SoftInputBudget = 0
	cfg.ModelProfiles["vision"] = p
	if err := cfg.ValidateServe(); err == nil || !strings.Contains(err.Error(), "model_profiles.vision.context.soft_input_budget") {
		t.Errorf("vision without soft_input_budget: err = %v, want the generic profile checks", err)
	}
	for _, name := range []string{"short_context", "long_context"} {
		cfg := mustLoad(t, "testdata/valid.yaml")
		p := cfg.ModelProfiles[name]
		p.Modalities = []string{"text"}
		cfg.ModelProfiles[name] = p
		if err := cfg.ValidateServe(); err != nil {
			t.Errorf("text-only %s: %v", name, err)
		}
	}
}

func TestValidate_PanelPlatformIsReserved(t *testing.T) {
	cfg := mustLoad(t, "testdata/valid.yaml")
	cfg.Bots[0].Platform = conloomapi.PlatformPanel
	err := cfg.ValidateServe()
	if want := "bots[0].platform"; err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("err = %v, want %s reported as reserved", err, want)
	}
}
