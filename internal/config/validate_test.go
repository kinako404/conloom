package config

import (
	"slices"
	"strings"
	"testing"
	"time"
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
