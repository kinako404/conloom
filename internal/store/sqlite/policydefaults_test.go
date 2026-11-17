package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestPolicyDefaults_AppendListLatest(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	created := time.Date(2026, 11, 18, 1, 2, 3, 4, time.UTC)
	var policies []conloomapi.GroupPolicy
	for i := 1; i <= 3; i++ {
		p := conloomapi.DefaultGroupPolicy()
		p.Context.MaxRecentMessages = 11 * i
		policies = append(policies, p)
		d, err := db.AppendPolicyDefaults(ctx, store.PolicyDefaults{
			Kind: conloomapi.ConvGroup, Policy: p, Reason: "tune", UpdatedBy: "panel:admin", CreatedAt: created,
		})
		if err != nil {
			t.Fatalf("AppendPolicyDefaults %d: %v", i, err)
		}
		if d.Version != i {
			t.Fatalf("appended group version = %d, want %d", d.Version, i)
		}
	}
	private, err := db.AppendPolicyDefaults(ctx, store.PolicyDefaults{
		Kind: conloomapi.ConvPrivate, Policy: conloomapi.DefaultGroupPolicy(), Reason: "init", UpdatedBy: "system",
	})
	if err != nil || private.Version != 1 || private.CreatedAt.IsZero() {
		t.Fatalf("private append = %+v, %v; want version 1 with a creation time", private, err)
	}

	list, err := db.ListPolicyDefaults(ctx, conloomapi.ConvGroup, 2)
	if err != nil {
		t.Fatalf("ListPolicyDefaults: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d, want 2", len(list))
	}
	for i, d := range list {
		want := 3 - i
		if d.Version != want || d.Kind != conloomapi.ConvGroup || d.Reason != "tune" || d.UpdatedBy != "panel:admin" ||
			!d.CreatedAt.Equal(created) || !reflect.DeepEqual(d.Policy, policies[want-1]) {
			t.Fatalf("list[%d] = %+v, want version %d", i, d, want)
		}
	}
	if all, err := db.ListPolicyDefaults(ctx, conloomapi.ConvGroup, 0); err != nil || len(all) != 3 {
		t.Fatalf("unlimited list = %d, %v; want 3", len(all), err)
	}
	if all, err := db.ListPolicyDefaults(ctx, conloomapi.ConvPrivate, 0); err != nil || len(all) != 1 || all[0].Version != 1 {
		t.Fatalf("private list = %+v, %v; want version 1 only", all, err)
	}

	latest, err := db.LatestPolicyDefaults(ctx, conloomapi.ConvGroup)
	if err != nil || latest.Version != 3 || latest.Policy.Context.MaxRecentMessages != 33 {
		t.Fatalf("LatestPolicyDefaults = %+v, %v; want version 3 with 33 recent messages", latest, err)
	}
	if _, err := db.LatestPolicyDefaults(ctx, "channel"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing kind err = %v, want ErrNotFound", err)
	}
	if list, err := db.ListPolicyDefaults(ctx, "channel", 0); err != nil || len(list) != 0 {
		t.Fatalf("missing kind list = %v, %v; want empty", list, err)
	}
}

func TestPolicyFollowers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a := panelConv("qq", conloomapi.ConvGroup, "10001")
	b := panelConv("qq", conloomapi.ConvGroup, "10002")
	c := panelConv("qq", conloomapi.ConvGroup, "10003")
	d := panelConv("qq", conloomapi.ConvGroup, "10004")
	p := panelConv("qq", conloomapi.ConvPrivate, "20001")
	seedConversations(t, db, a, b, c, d, p)
	appendPolicies := func(key string, follows ...bool) {
		t.Helper()
		for _, f := range follows {
			if _, err := db.AppendPolicyVersion(ctx, store.PolicyVersion{
				ConversationKey: key, Policy: conloomapi.DefaultGroupPolicy(), Reason: "edit", UpdatedBy: "29999",
				FollowsDefaults: f,
			}); err != nil {
				t.Fatalf("AppendPolicyVersion %s: %v", key, err)
			}
		}
	}
	appendPolicies(b.Key, false)
	appendPolicies(c.Key, false, true)
	appendPolicies(d.Key, true, false)

	got, err := db.PolicyFollowers(ctx, conloomapi.ConvGroup)
	if err != nil {
		t.Fatalf("PolicyFollowers group: %v", err)
	}
	if want := []string{a.Key, c.Key}; !slices.Equal(got, want) {
		t.Fatalf("group followers = %v, want %v", got, want)
	}
	got, err = db.PolicyFollowers(ctx, conloomapi.ConvPrivate)
	if err != nil {
		t.Fatalf("PolicyFollowers private: %v", err)
	}
	if want := []string{p.Key}; !slices.Equal(got, want) {
		t.Fatalf("private followers = %v, want %v", got, want)
	}
}

func TestInitPolicyDefaults(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	fileGroup := conloomapi.DefaultGroupPolicy()
	fileGroup.Context.MaxRecentMessages = 42
	imported := store.PolicyDefaults{Kind: conloomapi.ConvGroup, Policy: fileGroup, Reason: "从配置文件导入", UpdatedBy: "system"}
	builtinPrivate := conloomapi.DefaultGroupPolicy()
	builtinPrivate.Memory.SummaryMaxChars = 0
	builtins := []store.PolicyDefaults{
		{Kind: conloomapi.ConvGroup, Policy: conloomapi.DefaultGroupPolicy(), Reason: "内置默认值", UpdatedBy: "builtin"},
		{Kind: conloomapi.ConvPrivate, Policy: builtinPrivate, Reason: "内置默认值", UpdatedBy: "builtin"},
	}

	db := newTestDB(t)
	kinds, err := db.InitPolicyDefaults(ctx, []store.PolicyDefaults{imported}, builtins, now)
	if err != nil {
		t.Fatalf("InitPolicyDefaults: %v", err)
	}
	if !slices.Equal(kinds, []string{conloomapi.ConvGroup}) {
		t.Fatalf("imported %v, want [group]", kinds)
	}
	group, err := db.LatestPolicyDefaults(ctx, conloomapi.ConvGroup)
	if err != nil || group.Version != 1 || group.UpdatedBy != "system" || group.Reason != "从配置文件导入" ||
		!group.CreatedAt.Equal(now) || !reflect.DeepEqual(group.Policy, fileGroup) {
		t.Fatalf("group defaults = %+v, %v; want the imported row as version 1", group, err)
	}
	private, err := db.LatestPolicyDefaults(ctx, conloomapi.ConvPrivate)
	if err != nil || private.Version != 1 || private.UpdatedBy != "builtin" || !reflect.DeepEqual(private.Policy, builtinPrivate) {
		t.Fatalf("private defaults = %+v, %v; want the builtin row as version 1", private, err)
	}
	if v, ok, err := db.GetMeta(ctx, store.MetaConfigPolicyDefaultsMigrated); err != nil || !ok || v != fmtTime(now) {
		t.Fatalf("GetMeta = %q, %v, %v; want the import time %q", v, ok, err, fmtTime(now))
	}

	again := store.PolicyDefaults{Kind: conloomapi.ConvPrivate, Policy: fileGroup, Reason: "从配置文件导入", UpdatedBy: "system"}
	kinds, err = db.InitPolicyDefaults(ctx, []store.PolicyDefaults{again}, builtins, now.Add(time.Hour))
	if !errors.Is(err, store.ErrAlreadyMigrated) || kinds != nil {
		t.Fatalf("second InitPolicyDefaults = %v, %v; want ErrAlreadyMigrated", kinds, err)
	}
	if n := countRows(t, db, "policy_defaults"); n != 2 {
		t.Fatalf("policy_defaults rows after a refused import = %d, want 2", n)
	}
	if v, _, _ := db.GetMeta(ctx, store.MetaConfigPolicyDefaultsMigrated); v != fmtTime(now) {
		t.Fatalf("import time after a refused import = %q, want %q", v, fmtTime(now))
	}

	fresh := newTestDB(t)
	kinds, err = fresh.InitPolicyDefaults(ctx, nil, builtins, now)
	if err != nil || len(kinds) != 0 {
		t.Fatalf("builtins only = %v, %v; want nothing imported", kinds, err)
	}
	for _, b := range builtins {
		got, err := fresh.LatestPolicyDefaults(ctx, b.Kind)
		if err != nil || got.Version != 1 || got.UpdatedBy != "builtin" || !reflect.DeepEqual(got.Policy, b.Policy) {
			t.Fatalf("%s defaults = %+v, %v; want the builtin row as version 1", b.Kind, got, err)
		}
	}
	if _, ok, _ := fresh.GetMeta(ctx, store.MetaConfigPolicyDefaultsMigrated); ok {
		t.Error("writing builtins recorded the import marker")
	}
	kinds, err = fresh.InitPolicyDefaults(ctx, nil, builtins, now.Add(time.Hour))
	if err != nil || len(kinds) != 0 {
		t.Fatalf("builtins again = %v, %v; want skipped without error", kinds, err)
	}
	if n := countRows(t, fresh, "policy_defaults"); n != 2 {
		t.Fatalf("policy_defaults rows after builtins again = %d, want 2", n)
	}
}

func TestMigration0021_AddsTableAndFollowsColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	old, err := open(ctx, path, migrationsUpTo(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	beforeCols := tableInfos(t, old.r)
	if _, err := old.w.Exec(`INSERT INTO group_policies (conversation_key, version, policy, reason, updated_by, created_at)
		VALUES (?, 1, '{}', 'edit', '29999', ?)`, testConvKey, fmtTime(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := open(ctx, path, migrationsUpTo(t, 21))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	afterCols := tableInfos(t, db.r)
	for table, cols := range beforeCols {
		want := cols
		if table == "group_policies" {
			want = append(slices.Clone(cols), "6|follows_defaults|INTEGER|1|0|0")
		}
		if !slices.Equal(afterCols[table], want) {
			t.Errorf("%s columns %v, want %v", table, afterCols[table], want)
		}
	}
	want := []string{"0|kind|TEXT|1||1", "1|version|INTEGER|1||2", "2|policy|TEXT|1||0", "3|reason|TEXT|1||0",
		"4|updated_by|TEXT|1||0", "5|created_at|TEXT|1||0"}
	if !slices.Equal(afterCols["policy_defaults"], want) {
		t.Errorf("policy_defaults columns %v, want %v", afterCols["policy_defaults"], want)
	}
	if pv, err := db.LatestPolicy(ctx, testConvKey); err != nil || pv.Version != 1 || pv.FollowsDefaults {
		t.Fatalf("stored policy after 0021 = %+v, %v; want version 1 not following the defaults", pv, err)
	}
}
