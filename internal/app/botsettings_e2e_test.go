package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// liveLayoutBots are two config bots as a config file from before the bot
// settings moved into the database sets them.
const liveLayoutBots = `  - id: qq-main
    platform: qq
    name: 小明
    platform_self_id: "30001"
    admins: ["29999"]
    enabled_conversations:
      - {type: group, platform_id: "10001"}
      - {type: group, platform_id: "10002"}
      - {type: private, platform_id: "20001"}
    private_chat: allowlist
    persona_template: lighthouse
    skills: [memory, time]
  - id: tg-main
    platform: telegram
    name: 小红
    platform_self_id: "30002"
    admins: ["20001", "29999"]
    enabled_conversations:
      - {type: group, platform_id: "-1001234567890"}
    private_chat: all
    persona_template: default
    skills: [time]
`

// identityBots are liveLayoutBots with only the keys the config file keeps.
const identityBots = `  - {id: qq-main, platform: qq, platform_self_id: "30001"}
  - {id: tg-main, platform: telegram, platform_self_id: "30002"}
`

const botSettingsConfig = `server:
  listen: 127.0.0.1:0
storage:
  data_dir: DATA_DIR
api_tokens:
  - {name: qq, token: fake-qq-token, bot_instance_id: qq-main, scopes: [events:write, outbox:read]}
  - {name: tg, token: fake-tg-token, bot_instance_id: tg-main, scopes: [events:write, outbox:read]}
bots:
BOTS
model_profiles:
  short_context:
    endpoint: http://127.0.0.1:1/v1
    model: fake-model
    context: {native_window: 32768, configured_window: 32768, soft_input_budget: 16000, reserved_output_tokens: 1024, safety_margin_tokens: 1024}
`

// botSettingsCfg writes a config with the bots bots and the data directory
// dataDir and loads it.
func botSettingsCfg(t *testing.T, dataDir, bots string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conloom.yaml")
	yml := strings.NewReplacer("DATA_DIR", strconv.Quote(dataDir), "BOTS\n", bots).Replace(botSettingsConfig)
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	return cfg
}

// botsAsServed starts the app for cfg and returns the capabilities of
// qq-main and tg-main and the logs; the app is closed again.
func botsAsServed(t *testing.T, cfg *config.Config) (map[string]conloomapi.Capabilities, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(logs, nil)), app.Options{})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	srv := httptest.NewServer(a.Handler())
	defer func() {
		srv.Close()
		if err := a.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	out := map[string]conloomapi.Capabilities{}
	for id, token := range map[string]string{"qq-main": "fake-qq-token", "tg-main": "fake-tg-token"} {
		c, err := client.New(srv.URL, token, nil).Capabilities(context.Background())
		if err != nil {
			t.Fatalf("capabilities of %s: %v", id, err)
		}
		c.Scopes = nil
		out[id] = c
	}
	return out, logs
}

func storedBotSettings(t *testing.T, db *sqlite.DB) map[string]store.BotSettings {
	t.Helper()
	rows, err := db.ListBotSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.BotSettings{}
	for _, r := range rows {
		r.CreatedAt, r.UpdatedAt = time.Time{}, time.Time{}
		out[r.BotInstanceID] = r
	}
	return out
}

type botMigrateDetail struct {
	Migrated []string `json:"migrated"`
	Skipped  []string `json:"skipped"`
}

func botMigrateAudits(t *testing.T, db *sqlite.DB) []botMigrateDetail {
	t.Helper()
	events, err := db.ListAudit(context.Background(), store.AuditFilter{Kind: "bot.settings.migrate", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var out []botMigrateDetail
	for _, e := range events {
		var d botMigrateDetail
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil || e.Actor != "system" {
			t.Fatalf("bot settings migrate audit = %+v (%v), want one by system", e, err)
		}
		out = append(out, d)
	}
	return out
}

// TestBotSettings_MigrateLiveLayout upgrades a config file with two bots
// that set every moved key: the first start stores exactly the file's
// settings and records the migration, a start with the keys still in the
// file is refused, and once they are removed the bots behave as before.
func TestBotSettings_MigrateLiveLayout(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	legacyCfg := botSettingsCfg(t, dataDir, liveLayoutBots)
	db := openDB(t, legacyCfg)
	if err := db.PutPersonaTemplate(context.Background(), store.PersonaTemplateRecord{Name: "lighthouse",
		Persona: legacyTemplate("灯塔"), UpdatedBy: "panel:admin"}); err != nil {
		t.Fatal(err)
	}

	first, logs := botsAsServed(t, legacyCfg)
	want := map[string]store.BotSettings{
		"qq-main": {BotInstanceID: "qq-main", Name: "小明", Admins: []string{"29999"}, PrivateChat: "allowlist",
			PrivateAllowlist: []string{"20001"}, Groups: []string{"10001", "10002"}, PrivateTemplate: "lighthouse",
			GroupTemplate: "lighthouse", Skills: []string{"memory", "time"}, UpdatedBy: "system"},
		"tg-main": {BotInstanceID: "tg-main", Name: "小红", Admins: []string{"20001", "29999"}, PrivateChat: "all",
			PrivateAllowlist: []string{}, Groups: []string{"-1001234567890"}, PrivateTemplate: "default",
			GroupTemplate: "default", Skills: []string{"time"}, UpdatedBy: "system"},
	}
	if got := storedBotSettings(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored settings = %+v\nwant %+v", got, want)
	}
	if v, ok, err := db.GetMeta(context.Background(), store.MetaConfigBotSettingsMigrated); err != nil || !ok || v == "" {
		t.Errorf("migration marker = %q, %v, %v; want it set", v, ok, err)
	}
	if audits := botMigrateAudits(t, db); len(audits) != 1 || !slices.Equal(audits[0].Migrated, []string{"qq-main", "tg-main"}) ||
		audits[0].Skipped == nil || len(audits[0].Skipped) != 0 {
		t.Errorf("migrate audits = %+v, want one migrating both bots", audits)
	}
	if n := logLines(logs, "WARN", "remove name, admins"); n != 1 {
		t.Errorf("warnings to remove the keys = %d, want 1:\n%s", n, logs)
	}
	wantQQ := conloomapi.Capabilities{SchemaVersion: conloomapi.SchemaVersion, BotInstanceID: "qq-main", BotName: "小明",
		PlatformSelfID: "30001", EventTypes: first["qq-main"].EventTypes, ActionTypes: first["qq-main"].ActionTypes,
		TriggerTypes: first["qq-main"].TriggerTypes, PrivateChat: "allowlist",
		EnabledConversations: []conloomapi.ConversationRef{{Type: "group", PlatformID: "10001"},
			{Type: "group", PlatformID: "10002"}, {Type: "private", PlatformID: "20001"}}}
	if !reflect.DeepEqual(first["qq-main"], wantQQ) {
		t.Errorf("qq-main after the migration = %+v\nwant %+v: as the file set it", first["qq-main"], wantQQ)
	}
	if c := first["tg-main"]; c.BotName != "小红" || c.PrivateChat != "all" ||
		!slices.Equal(c.EnabledConversations, []conloomapi.ConversationRef{{Type: "group", PlatformID: "-1001234567890"}}) {
		t.Errorf("tg-main after the migration = %+v, want it as the file set it", c)
	}

	_, err := startAndClose(t, legacyCfg)
	if !errors.Is(err, store.ErrAlreadyMigrated) {
		t.Fatalf("second start with the keys still in the file: err = %v, want ErrAlreadyMigrated", err)
	}

	again, _ := botsAsServed(t, botSettingsCfg(t, dataDir, identityBots))
	if !reflect.DeepEqual(again, first) {
		t.Errorf("bots after the keys were removed = %+v\nwant them as before %+v", again, first)
	}
	if got := storedBotSettings(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("stored settings after the restarts = %+v, want them unchanged", got)
	}
	if audits := botMigrateAudits(t, db); len(audits) != 1 {
		t.Errorf("migrate audits = %+v, want only the first start's", audits)
	}
	insts, err := db.ListBotInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantInsts := []store.BotInstance{{ID: "qq-main", Platform: "qq", Name: "小明"}, {ID: "tg-main", Platform: "telegram", Name: "小红"}}
	if !reflect.DeepEqual(insts, wantInsts) {
		t.Errorf("bot instances = %+v, want %+v", insts, wantInsts)
	}
}

// TestBotSettings_SecondStartRefuses checks that once the settings were
// migrated, a config file that still has a moved key is refused with a
// Chinese message naming it, leaving the stored settings alone, and that the
// stored settings take effect once the key is removed.
func TestBotSettings_SecondStartRefuses(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if _, err := startAndClose(t, botSettingsCfg(t, dataDir, liveLayoutBots)); err != nil {
		t.Fatalf("first start: %v", err)
	}
	cfg := botSettingsCfg(t, dataDir, identityBots)
	db := openDB(t, cfg)
	edited, err := db.GetBotSettings(context.Background(), "qq-main")
	if err != nil {
		t.Fatal(err)
	}
	edited.Name, edited.Groups, edited.UpdatedBy = "小明二号", []string{"10003"}, "panel:admin"
	if err := db.PutBotSettings(context.Background(), edited); err != nil {
		t.Fatal(err)
	}

	oneKey := strings.Replace(identityBots, `platform_self_id: "30002"}`, `platform_self_id: "30002", private_chat: off}`, 1)
	_, err = startAndClose(t, botSettingsCfg(t, dataDir, oneKey))
	if !errors.Is(err, store.ErrAlreadyMigrated) {
		t.Fatalf("start with private_chat still in the file: err = %v, want ErrAlreadyMigrated", err)
	}
	for _, want := range []string{"已在 ", "导入数据库", "Web 管理面板", "请删除配置文件里 bots[1]（tg-main）的 private_chat，",
		"只留 id、platform、platform_self_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "qq-main") {
		t.Errorf("error %q names qq-main, which has no moved key left", err)
	}
	if got, err := db.GetBotSettings(context.Background(), "qq-main"); err != nil || got.Name != "小明二号" {
		t.Errorf("qq-main after a refused start = %+v, %v; want the panel's edit kept", got, err)
	}
	if tg, err := db.GetBotSettings(context.Background(), "tg-main"); err != nil || tg.PrivateChat != "all" {
		t.Errorf("tg-main after a refused start = %+v, %v; want private_chat all, not the file's off", tg, err)
	}
	if audits := botMigrateAudits(t, db); len(audits) != 1 {
		t.Errorf("migrate audits = %+v, want only the first start's", audits)
	}

	got, _ := botsAsServed(t, cfg)
	if c := got["qq-main"]; c.BotName != "小明二号" ||
		!slices.Equal(c.EnabledConversations, []conloomapi.ConversationRef{{Type: "group", PlatformID: "10003"}, {Type: "private", PlatformID: "20001"}}) {
		t.Errorf("qq-main = %+v, want the stored settings", c)
	}
}

// TestBotSettings_NewConfigBotDefaults checks that a config bot without
// stored settings runs with the defaults, both on a new install and when it
// is added after the migration, without storing anything for it.
func TestBotSettings_NewConfigBotDefaults(t *testing.T) {
	defaults := func(id, selfID string) conloomapi.Capabilities {
		return conloomapi.Capabilities{BotInstanceID: id, BotName: id, PlatformSelfID: selfID, PrivateChat: "admins",
			EnabledConversations: []conloomapi.ConversationRef{}}
	}
	same := func(got, want conloomapi.Capabilities) bool {
		return got.BotInstanceID == want.BotInstanceID && got.BotName == want.BotName && got.PlatformSelfID == want.PlatformSelfID &&
			got.PrivateChat == want.PrivateChat && slices.Equal(got.EnabledConversations, want.EnabledConversations) &&
			got.EnabledConversations != nil
	}

	fresh := botSettingsCfg(t, filepath.Join(t.TempDir(), "data"), identityBots)
	got, logs := botsAsServed(t, fresh)
	for id, selfID := range map[string]string{"qq-main": "30001", "tg-main": "30002"} {
		if want := defaults(id, selfID); !same(got[id], want) {
			t.Errorf("%s on a new install = %+v, want the defaults %+v", id, got[id], want)
		}
	}
	db := openDB(t, fresh)
	if rows := storedBotSettings(t, db); len(rows) != 0 {
		t.Errorf("stored settings = %+v, want none: the defaults are not stored", rows)
	}
	if _, ok, err := db.GetMeta(context.Background(), store.MetaConfigBotSettingsMigrated); err != nil || ok {
		t.Errorf("migration marker set = %v, %v; want nothing recorded without moved keys", ok, err)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("warnings on a new install:\n%s", logs)
	}

	// qq-main migrated from an old file; tg-main added afterwards.
	dataDir := filepath.Join(t.TempDir(), "data")
	onlyQQ := botSettingsCfg(t, dataDir, liveLayoutBots)
	onlyQQ.Bots = onlyQQ.Bots[:1]
	onlyQQ.APITokens = onlyQQ.APITokens[:1]
	if _, err := startAndClose(t, onlyQQ); err != nil {
		t.Fatalf("first start with qq-main only: %v", err)
	}
	got, _ = botsAsServed(t, botSettingsCfg(t, dataDir, identityBots))
	if c := got["qq-main"]; c.BotName != "小明" || c.PrivateChat != "allowlist" {
		t.Errorf("migrated qq-main = %+v, want its migrated settings", c)
	}
	if want := defaults("tg-main", "30002"); !same(got["tg-main"], want) {
		t.Errorf("tg-main added after the migration = %+v, want the defaults %+v", got["tg-main"], want)
	}
	db = openDB(t, onlyQQ)
	if rows := storedBotSettings(t, db); len(rows) != 1 || rows["qq-main"].Name != "小明" {
		t.Errorf("stored settings = %+v, want qq-main's only", rows)
	}
	insts, err := db.ListBotInstances(context.Background())
	if err != nil || len(insts) != 2 || insts[1] != (store.BotInstance{ID: "tg-main", Platform: "telegram", Name: "tg-main"}) {
		t.Errorf("bot instances = %+v, %v; want tg-main registered under its ID as name", insts, err)
	}
}
