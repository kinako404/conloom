package sqlite

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func listBotSettings(t *testing.T, db *DB) []store.BotSettings {
	t.Helper()
	got, err := db.ListBotSettings(context.Background())
	if err != nil {
		t.Fatalf("ListBotSettings: %v", err)
	}
	return got
}

func TestBotSettings_PutGetList(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if got := listBotSettings(t, db); len(got) != 0 {
		t.Fatalf("ListBotSettings of a new database = %+v, want none", got)
	}
	if _, err := db.GetBotSettings(ctx, "qq-main"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetBotSettings without settings = %v, want ErrNotFound", err)
	}

	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	qq := store.BotSettings{BotInstanceID: "qq-main", Name: "小明", Admins: []string{"29999"}, PrivateChat: "allowlist",
		PrivateAllowlist: []string{"20001"}, Groups: []string{"10001", "10002"}, PrivateTemplate: "lighthouse",
		GroupTemplate: "default", Skills: []string{"memory", "time"}, UpdatedBy: "system", CreatedAt: at, UpdatedAt: at}
	tg := store.BotSettings{BotInstanceID: "tg-main", Name: "小红", PrivateChat: "admins", PrivateTemplate: "default",
		GroupTemplate: "default", UpdatedBy: "system", UpdatedAt: at}
	for _, s := range []store.BotSettings{tg, qq} {
		if err := db.PutBotSettings(ctx, s); err != nil {
			t.Fatalf("PutBotSettings %s: %v", s.BotInstanceID, err)
		}
	}
	tg.Admins, tg.PrivateAllowlist, tg.Groups, tg.Skills, tg.CreatedAt = []string{}, []string{}, []string{}, []string{}, at
	if got := listBotSettings(t, db); !reflect.DeepEqual(got, []store.BotSettings{qq, tg}) {
		t.Fatalf("ListBotSettings = %+v\nwant %+v (nil lists read as empty, by bot ID)", got, []store.BotSettings{qq, tg})
	}
	if got, err := db.GetBotSettings(ctx, "qq-main"); err != nil || !reflect.DeepEqual(got, qq) {
		t.Fatalf("GetBotSettings = %+v, %v; want %+v", got, err, qq)
	}

	later := at.Add(time.Hour)
	edited := qq
	edited.Name, edited.Admins, edited.PrivateChat, edited.PrivateAllowlist = "小明二号", []string{"29999", "20001"}, "all", nil
	edited.Groups, edited.PrivateTemplate, edited.GroupTemplate, edited.Skills = nil, "default", "lighthouse", nil
	edited.UpdatedBy, edited.CreatedAt, edited.UpdatedAt = "panel:admin", later, later
	if err := db.PutBotSettings(ctx, edited); err != nil {
		t.Fatal(err)
	}
	edited.PrivateAllowlist, edited.Groups, edited.Skills, edited.CreatedAt = []string{}, []string{}, []string{}, at
	if got, err := db.GetBotSettings(ctx, "qq-main"); err != nil || !reflect.DeepEqual(got, edited) {
		t.Fatalf("GetBotSettings after a replace = %+v, %v; want %+v (CreatedAt kept)", got, err, edited)
	}
}

func TestMigrateConfigBotSettings_StoresMissingKeepsStoredOnce(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	online := store.BotSettings{BotInstanceID: "tg-main", Name: "面板改过", Admins: []string{"20001"}, PrivateChat: "all",
		PrivateAllowlist: []string{}, Groups: []string{}, PrivateTemplate: "default", GroupTemplate: "default",
		Skills: []string{}, UpdatedBy: "panel:admin", CreatedAt: at, UpdatedAt: at}
	if err := db.PutBotSettings(ctx, online); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	qq := store.BotSettings{BotInstanceID: "qq-main", Name: "小明", Admins: []string{"29999"}, PrivateChat: "admins",
		PrivateAllowlist: []string{}, Groups: []string{"10001"}, PrivateTemplate: "lighthouse", GroupTemplate: "lighthouse",
		Skills: []string{"memory"}, UpdatedBy: "system"}
	fromFile := store.BotSettings{BotInstanceID: "tg-main", Name: "配置文件", PrivateChat: "off", UpdatedBy: "system"}
	migrated, err := db.MigrateConfigBotSettings(ctx, []store.BotSettings{qq, fromFile}, now)
	if err != nil {
		t.Fatalf("MigrateConfigBotSettings: %v", err)
	}
	if !slices.Equal(migrated, []string{"qq-main"}) {
		t.Fatalf("migrated %v, want qq-main only: tg-main has stored settings", migrated)
	}
	qq.CreatedAt, qq.UpdatedAt = now, now
	if got := listBotSettings(t, db); !reflect.DeepEqual(got, []store.BotSettings{qq, online}) {
		t.Fatalf("settings = %+v\nwant %+v", got, []store.BotSettings{qq, online})
	}
	if v, ok, err := db.GetMeta(ctx, store.MetaConfigBotSettingsMigrated); err != nil || !ok || v != fmtTime(now) {
		t.Fatalf("GetMeta = %q, %v, %v; want the migration time %q", v, ok, err, fmtTime(now))
	}
	if _, ok, _ := db.GetMeta(ctx, store.MetaConfigTemplatesMigrated); ok {
		t.Error("the bot settings migration recorded the persona templates' marker")
	}

	again := []store.BotSettings{{BotInstanceID: "qq-other", Name: "小刚", PrivateChat: "admins", UpdatedBy: "system"}}
	migrated, err = db.MigrateConfigBotSettings(ctx, again, now.Add(time.Hour))
	if !errors.Is(err, store.ErrAlreadyMigrated) || migrated != nil {
		t.Fatalf("second MigrateConfigBotSettings = %v, %v; want ErrAlreadyMigrated", migrated, err)
	}
	if got := listBotSettings(t, db); len(got) != 2 {
		t.Fatalf("settings after a refused migration = %+v, want the two from before", got)
	}
	if v, _, _ := db.GetMeta(ctx, store.MetaConfigBotSettingsMigrated); v != fmtTime(now) {
		t.Fatalf("migration time after a refused migration = %q, want %q", v, fmtTime(now))
	}
}

func TestMigration0019_OnlyAddsTables(t *testing.T) {
	_, added, after := migrateOnlyAdding(t, 19)
	if want := []string{"bot_settings", "sqlite_autoindex_bot_settings_1"}; !slices.Equal(added, want) {
		t.Fatalf("added %v, want %v", added, want)
	}
	want := []string{"0|bot_instance_id|TEXT|0||1", "1|name|TEXT|1||0", "2|admins|TEXT|1||0", "3|private_chat|TEXT|1||0",
		"4|private_allowlist|TEXT|1||0", "5|enabled_groups|TEXT|1||0", "6|private_template|TEXT|1||0",
		"7|group_template|TEXT|1||0", "8|skills|TEXT|1||0", "9|updated_by|TEXT|1||0", "10|created_at|TEXT|1||0",
		"11|updated_at|TEXT|1||0"}
	if !slices.Equal(after["bot_settings"], want) {
		t.Errorf("bot_settings columns %v, want %v", after["bot_settings"], want)
	}
}
