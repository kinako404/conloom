package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExitCodes_UsageAndConfigAreDistinct(t *testing.T) {
	if exitUsage != 2 || exitConfig != 78 {
		t.Fatalf("exitUsage = %d, exitConfig = %d; want 2 and 78 (EX_CONFIG), which no Go panic uses", exitUsage, exitConfig)
	}

	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	empty := write("empty.yaml", "storage:\n  data_dir: ./data\n")
	unknownKey := write("unknown.yaml", "no_such_section: {}\n")
	mockCfg := write("mock.yaml", `bots:
  - {id: bot_main, platform: qq}
adapters:
  mock: {core_url: "http://127.0.0.1:9", core_token: fake-adapter-token, bot_instance_id: bot_main}
`)
	missing := filepath.Join(dir, "missing.yaml")
	existing := write("existing.db", "")

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no command", nil, exitUsage},
		{"unknown command", []string{"nosuch"}, exitUsage},
		{"unknown flag", []string{"serve", "-x"}, exitUsage},
		{"extra argument", []string{"serve", "-c", empty, "extra"}, exitUsage},
		{"missing adapter name", []string{"adapter"}, exitUsage},
		{"unknown adapter", []string{"adapter", "nosuch"}, exitUsage},
		{"backup without -o", []string{"backup", "-c", empty}, exitUsage},
		{"backup over an existing file", []string{"backup", "-c", empty, "-o", existing}, exitUsage},
		{"mock with a bad -user", []string{"mock", "-c", mockCfg, "-user", ":x"}, exitUsage},
		{"mock with a bad -conv", []string{"mock", "-c", mockCfg, "-user", "20001:小明", "-conv", "channel:1"}, exitUsage},

		{"serve: missing config file", []string{"serve", "-c", missing}, exitConfig},
		{"serve: unknown config key", []string{"serve", "-c", unknownKey}, exitConfig},
		{"serve: invalid config", []string{"serve", "-c", empty}, exitConfig},
		{"adapter napcat: invalid config", []string{"adapter", "napcat", "-c", empty}, exitConfig},
		{"adapter telegram: invalid config", []string{"adapter", "telegram", "-c", empty}, exitConfig},
		{"mock: invalid config", []string{"mock", "-c", empty}, exitConfig},
		{"mock: bot without admins and no -user", []string{"mock", "-c", mockCfg}, exitConfig},
		{"backup: missing config file", []string{"backup", "-c", missing, "-o", filepath.Join(dir, "out.db")}, exitConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCommand(context.Background(), tc.args...)
			if code != tc.want {
				t.Errorf("conloom %q: exit %d, want %d; stderr %s", tc.args, code, tc.want, stderr)
			}
		})
	}
}
