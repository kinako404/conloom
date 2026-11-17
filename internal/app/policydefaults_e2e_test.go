package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	bothPolicySections = `default_group_policy:
  memory: {enabled: true}
default_private_policy:
  reply: {max_parts: 5}
`
	groupPolicySection = `default_group_policy:
  context: {max_recent_messages: 33}
  memory: {summary_max_chars: 1500}
`
	privatePolicySection = `default_private_policy:
  reply: {max_parts: 5}
`
)

// policyDefaultsConfig loads configTemplate for the data directory dataDir
// followed by sections, without the test default policies of testConfig.
func policyDefaultsConfig(t *testing.T, dataDir, sections string) *config.Config {
	t.Helper()
	return loadTestConfig(t, configTemplate, configParams{dataDir: dataDir, modelURL: "http://127.0.0.1:1"}, sections)
}

// withDB calls fn with the database of cfg and closes it again.
func withDB(t *testing.T, cfg *config.Config, fn func(db *sqlite.DB)) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	fn(db)
}

func latestDefaults(t *testing.T, db *sqlite.DB, kind string) store.PolicyDefaults {
	t.Helper()
	d, err := db.LatestPolicyDefaults(context.Background(), kind)
	if err != nil {
		t.Fatalf("LatestPolicyDefaults(%s): %v", kind, err)
	}
	return d
}

// policyMigrateAudits returns the imported kinds of each settings.policy.migrate audit event.
func policyMigrateAudits(t *testing.T, db *sqlite.DB) [][]string {
	t.Helper()
	events, err := db.ListAudit(context.Background(), store.AuditFilter{Kind: "settings.policy.migrate", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, e := range events {
		var d struct {
			Imported []string `json:"imported"`
		}
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil || e.Actor != "system" {
			t.Fatalf("policy migrate audit = %+v (%v), want one by system", e, err)
		}
		out = append(out, d.Imported)
	}
	return out
}

func TestPolicyDefaults_ImportOnFirstStart(t *testing.T) {
	cfg := policyDefaultsConfig(t, t.TempDir(), bothPolicySections)
	logs, err := startAndClose(t, cfg)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	withDB(t, cfg, func(db *sqlite.DB) {
		group := latestDefaults(t, db, conloomapi.ConvGroup)
		if group.Version != 1 || group.UpdatedBy != "system" || group.Reason != "从配置文件导入" || !group.Policy.Memory.Enabled {
			t.Errorf("group defaults = %+v; want the file's section imported as version 1 by system", group)
		}
		private := latestDefaults(t, db, conloomapi.ConvPrivate)
		if private.Version != 1 || private.UpdatedBy != "system" || private.Reason != "从配置文件导入" || private.Policy.Reply.MaxParts != 5 {
			t.Errorf("private defaults = %+v; want the file's section imported as version 1 by system", private)
		}
		if _, ok, err := db.GetMeta(context.Background(), store.MetaConfigPolicyDefaultsMigrated); err != nil || !ok {
			t.Errorf("import marker = %v, %v; want it recorded", ok, err)
		}
		if got := policyMigrateAudits(t, db); len(got) != 1 || !slices.Equal(got[0], []string{"group", "private"}) {
			t.Errorf("migrate audits = %v, want one importing [group private]", got)
		}
	})
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "remove default_group_policy and default_private_policy from the config file") {
		t.Errorf("logs lack the warning to remove the sections:\n%s", out)
	}
	if n := strings.Count(out, "default policy imported from the config file"); n != 2 {
		t.Errorf("import log lines = %d, want 2:\n%s", n, out)
	}
}

func TestPolicyDefaults_FreshInstallSeedsBuiltins(t *testing.T) {
	cfg := policyDefaultsConfig(t, t.TempDir(), "")
	logs, err := startAndClose(t, cfg)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	withDB(t, cfg, func(db *sqlite.DB) {
		group := latestDefaults(t, db, conloomapi.ConvGroup)
		if group.Version != 1 || group.UpdatedBy != "system" || group.Reason != "内置默认值" ||
			!reflect.DeepEqual(group.Policy, conloomapi.DefaultGroupPolicy()) {
			t.Errorf("group defaults = %+v; want the built-in group defaults as version 1", group)
		}
		private := latestDefaults(t, db, conloomapi.ConvPrivate)
		if private.Version != 1 || private.UpdatedBy != "system" || private.Reason != "内置默认值" ||
			!reflect.DeepEqual(private.Policy, conloomapi.DefaultPrivatePolicy()) {
			t.Errorf("private defaults = %+v; want the built-in private defaults as version 1", private)
		}
		if _, ok, _ := db.GetMeta(context.Background(), store.MetaConfigPolicyDefaultsMigrated); ok {
			t.Error("a fresh install recorded the import marker")
		}
		if got := policyMigrateAudits(t, db); len(got) != 0 {
			t.Errorf("migrate audits = %v, want none", got)
		}
	})
	if out := logs.String(); strings.Contains(out, "remove default_group_policy") {
		t.Errorf("a fresh install warned about the sections:\n%s", out)
	}
}

func TestPolicyDefaults_OnlyGroupSection(t *testing.T) {
	cfg := policyDefaultsConfig(t, t.TempDir(), groupPolicySection)
	if _, err := startAndClose(t, cfg); err != nil {
		t.Fatalf("app.New: %v", err)
	}
	withDB(t, cfg, func(db *sqlite.DB) {
		group := latestDefaults(t, db, conloomapi.ConvGroup)
		if group.Reason != "从配置文件导入" || group.Policy.Context.MaxRecentMessages != 33 || group.Policy.Memory.SummaryMaxChars != 1500 {
			t.Errorf("group defaults = %+v; want the file's section imported", group)
		}
		private := latestDefaults(t, db, conloomapi.ConvPrivate)
		if private.Reason != "内置默认值" || !reflect.DeepEqual(private.Policy, conloomapi.DefaultPrivatePolicy()) ||
			private.Policy.Context.MaxRecentMessages == 33 || private.Policy.Memory.SummaryMaxChars != 0 {
			t.Errorf("private defaults = %+v; want the built-in private defaults, not the group section", private)
		}
		if got := policyMigrateAudits(t, db); len(got) != 1 || !slices.Equal(got[0], []string{"group"}) {
			t.Errorf("migrate audits = %v, want one importing [group]", got)
		}
	})
}

func TestPolicyDefaults_RestartWithKeysFails(t *testing.T) {
	requireOneVersionEach := func(t *testing.T, cfg *config.Config) {
		t.Helper()
		withDB(t, cfg, func(db *sqlite.DB) {
			for _, kind := range []string{conloomapi.ConvGroup, conloomapi.ConvPrivate} {
				if d := latestDefaults(t, db, kind); d.Version != 1 {
					t.Errorf("%s defaults after the refused start = version %d, want 1", kind, d.Version)
				}
			}
		})
	}

	t.Run("imported", func(t *testing.T) {
		cfg := policyDefaultsConfig(t, t.TempDir(), groupPolicySection)
		if _, err := startAndClose(t, cfg); err != nil {
			t.Fatalf("first app.New: %v", err)
		}
		_, err := startAndClose(t, cfg)
		if !errors.Is(err, store.ErrAlreadyMigrated) {
			t.Fatalf("second app.New = %v, want ErrAlreadyMigrated", err)
		}
		for _, want := range []string{"default_group_policy", "设置页", "已在 ", "从配置文件导入"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "default_private_policy") {
			t.Errorf("error %q names the private section the file does not have", err)
		}
		requireOneVersionEach(t, cfg)
	})

	t.Run("built-in", func(t *testing.T) {
		dataDir := t.TempDir()
		if _, err := startAndClose(t, policyDefaultsConfig(t, dataDir, "")); err != nil {
			t.Fatalf("first app.New: %v", err)
		}
		cfg := policyDefaultsConfig(t, dataDir, privatePolicySection)
		_, err := startAndClose(t, cfg)
		if !errors.Is(err, store.ErrAlreadyMigrated) {
			t.Fatalf("app.New with the private section = %v, want ErrAlreadyMigrated", err)
		}
		for _, want := range []string{"内置默认值", "default_private_policy", "设置页"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		}
		requireOneVersionEach(t, cfg)
	})
}

func TestPolicyDefaults_RestartWithoutKeysUsesDB(t *testing.T) {
	dataDir := t.TempDir()
	cfg := policyDefaultsConfig(t, dataDir, bothPolicySections)
	if _, err := startAndClose(t, cfg); err != nil {
		t.Fatalf("first app.New: %v", err)
	}
	withDB(t, cfg, func(db *sqlite.DB) {
		p := latestDefaults(t, db, conloomapi.ConvGroup).Policy
		p.Context.MaxRecentMessages = 33
		if _, err := db.AppendPolicyDefaults(context.Background(), store.PolicyDefaults{
			Kind: conloomapi.ConvGroup, Policy: p, Reason: "调小", UpdatedBy: "panel:admin",
		}); err != nil {
			t.Fatal(err)
		}
	})

	cfg = policyDefaultsConfig(t, dataDir, "")
	cfg.APITokens = append(cfg.APITokens, config.APIToken{
		Name: "admin", Token: adminToken, BotInstanceID: botID, Scopes: []string{conloomapi.ScopeAdminRead},
	})
	seedBotSettings(t, cfg, nil)
	e := newEnv(t, cfg)
	if resp, _ := e.say(group, alice, "大家好", mock.SayOptions{}); resp.Disposition != conloomapi.DispositionObserve {
		t.Fatalf("plain message: response %+v, want observe", resp)
	}
	pol, err := client.New(e.srv.URL, adminToken, nil).GetPolicy(context.Background(), "group_10001")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if pol.Version != 0 || pol.Policy.Context.MaxRecentMessages != 33 || !pol.Policy.Memory.Enabled {
		t.Fatalf("policy of a group without its own = %+v, want the stored default policy (33 recent messages, memory on)", pol)
	}
}
