package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type defaultsResp struct {
	Version   int                    `json:"version"`
	Policy    conloomapi.GroupPolicy `json:"policy"`
	Reason    string                 `json:"reason"`
	UpdatedBy string                 `json:"updated_by"`
	CreatedAt *time.Time             `json:"created_at"`
}

type settingsSectionResp struct {
	Current      defaultsResp    `json:"current"`
	Versions     []defaultsResp  `json:"versions"`
	Followers    int             `json:"followers"`
	MemoryLimits *map[string]int `json:"memory_limits"`
}

type settingsResp struct {
	Group   settingsSectionResp `json:"group"`
	Private settingsSectionResp `json:"private"`
}

type settingsSaveResp struct {
	Current  *defaultsResp `json:"current"`
	Warnings []string      `json:"warnings"`
}

const settingsPath = "/panel/api/settings/policies"

// setDefaults stores p as a new version of kind's default policy.
func (h *writeHarness) setDefaults(kind string, p conloomapi.GroupPolicy) store.PolicyDefaults {
	h.t.Helper()
	d, err := h.policies.SetDefaults(context.Background(), kind, p, "测试", "panel:"+testUser, "update")
	if err != nil {
		h.t.Fatalf("SetDefaults %s: %v", kind, err)
	}
	return d
}

// save sends a write that must answer 200 with the saved default policy and a
// list of warnings.
func (h *writeHarness) save(method, path string, body any) settingsSaveResp {
	h.t.Helper()
	w := h.write(method, path, body)
	if w.Code != http.StatusOK {
		h.t.Fatalf("%s %s: status %d, body %s", method, path, w.Code, w.Body)
	}
	var resp settingsSaveResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Current == nil {
		h.t.Fatalf("decode %s: %v", w.Body, err)
	}
	if resp.Warnings == nil {
		h.t.Fatalf("warnings is null in %s, want a list", w.Body)
	}
	return resp
}

// settingsAudit decodes the detail of the settings.policy.update events, by version newest first.
func (h *writeHarness) settingsAudit() []struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`
	Action  string `json:"action"`
} {
	h.t.Helper()
	events := h.audits("settings.policy.update")
	out := make([]struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
		Action  string `json:"action"`
	}, len(events))
	for i, e := range events {
		if e.Actor != "panel:"+testUser {
			h.t.Errorf("settings.policy.update actor = %q, want panel:%s", e.Actor, testUser)
		}
		if err := json.Unmarshal([]byte(e.Detail), &out[i]); err != nil {
			h.t.Fatalf("audit detail %q: %v", e.Detail, err)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

func TestSettingsPolicies_Get(t *testing.T) {
	h := newWriteHarness(t, nil)
	ctx := context.Background()
	for i := 1; i <= 55; i++ {
		h.setDefaults(policy.KindGroup, policyWith(func(p *conloomapi.GroupPolicy) { p.Context.MaxRecentMessages = 10 + i }))
	}
	h.setDefaults(policy.KindPrivate, conloomapi.DefaultPrivatePolicy())
	h.policies.Wait()
	own := h.seedConv("qq", conloomapi.ConvGroup, "10003")
	if _, err := h.policies.Update(ctx, own.Key, conloomapi.PolicyPut{Policy: conloomapi.DefaultGroupPolicy()}, "panel:"+testUser); err != nil {
		t.Fatal(err)
	}

	var got settingsResp
	raw := h.getJSON(settingsPath, &got)
	g := got.Group
	if g.Current.Version != 55 || g.Current.Policy.Context.MaxRecentMessages != 65 || g.Current.Reason != "测试" ||
		g.Current.UpdatedBy != "panel:"+testUser || g.Current.CreatedAt == nil {
		t.Errorf("group current = %+v, want v55", g.Current)
	}
	if len(g.Versions) != 50 || g.Versions[0].Version != 55 || g.Versions[49].Version != 6 {
		t.Errorf("group versions: %d, want 50 from v55 down", len(g.Versions))
	}
	if g.Followers != 1 {
		t.Errorf("group followers = %d, want 1 (writeGroup; 10003 has its own policy)", g.Followers)
	}
	if got.Private.Current.Version != 1 || len(got.Private.Versions) != 1 || got.Private.Followers != 0 {
		t.Errorf("private = %+v, want v1 without followers", got.Private)
	}
	for name, s := range map[string]settingsSectionResp{"group": g, "private": got.Private} {
		if s.MemoryLimits == nil {
			t.Fatalf("%s memory_limits missing: %s", name, raw)
		}
		for _, k := range []string{"summary_max_chars", "note_max_chars", "default_summary_max_chars", "default_note_max_chars"} {
			if (*s.MemoryLimits)[k] <= 0 {
				t.Errorf("%s memory_limits.%s = %d in %s", name, k, (*s.MemoryLimits)[k], raw)
			}
		}
	}
	if l := *got.Private.MemoryLimits; l["default_summary_max_chars"] != 3000 || l["default_note_max_chars"] != 1000 {
		t.Errorf("private memory_limits = %v, want the private defaults 3000/1000", l)
	}

	fresh := newWriteHarness(t, nil)
	raw = fresh.getJSON(settingsPath, &got)
	if got.Group.Current.Version != 0 || got.Group.Current.CreatedAt != nil || got.Group.Versions == nil || len(got.Group.Versions) != 0 {
		t.Errorf("nothing stored: %s, want version 0 with created_at null and versions []", raw)
	}
}

func TestSettingsPolicies_Put(t *testing.T) {
	h := newWriteHarness(t, nil)
	resp := h.save(http.MethodPut, settingsPath+"/group",
		map[string]any{"policy": map[string]any{"context": map[string]any{"max_recent_messages": 33}}, "reason": "调小"})
	if resp.Current.Version != 1 || resp.Current.Reason != "调小" || resp.Current.UpdatedBy != "panel:"+testUser ||
		resp.Current.CreatedAt == nil || len(resp.Warnings) != 0 {
		t.Errorf("PUT = %+v", resp)
	}
	want := conloomapi.DefaultGroupPolicy()
	want.Context.MaxRecentMessages = 33
	if d := h.policies.Defaults(policy.KindGroup); d.Version != 1 || !reflect.DeepEqual(d.Policy, want) {
		t.Errorf("stored defaults = v%d %+v, want the built-in group policy with 33", d.Version, d.Policy)
	}
	if !reflect.DeepEqual(resp.Current.Policy, want) {
		t.Errorf("response policy = %+v", resp.Current.Policy)
	}
	if a := h.settingsAudit(); len(a) != 1 || a[0].Kind != "group" || a[0].Version != 1 || a[0].Action != "update" {
		t.Errorf("settings.policy.update audits = %+v", a)
	}
	h.policies.Wait()

	var pol policyResp
	h.getJSON(convPath(writeGroup.Key, "policy"), &pol)
	if pol.Current.Policy.Context.MaxRecentMessages != 33 || !pol.Current.FollowsDefaults ||
		pol.Defaults == nil || pol.Defaults.Version != 1 {
		t.Errorf("follower policy = %+v defaults %+v, want 33, follows_defaults and defaults v1", pol.Current, pol.Defaults)
	}

	resp = h.save(http.MethodPut, settingsPath+"/private",
		map[string]any{"policy": map[string]any{"reply": map[string]any{"max_parts": 2}}})
	wantPrivate := conloomapi.DefaultPrivatePolicy()
	wantPrivate.Reply.MaxParts = 2
	if resp.Current.Version != 1 || !reflect.DeepEqual(resp.Current.Policy, wantPrivate) {
		t.Errorf("PUT private = v%d %+v, want the built-in private policy with max_parts 2", resp.Current.Version, resp.Current.Policy)
	}
}

func TestSettingsPolicies_PutInvalid(t *testing.T) {
	h := newWriteHarness(t, nil)
	h.save(http.MethodPut, settingsPath+"/group", map[string]any{"policy": map[string]any{}})
	body := errorBody(t, h.write(http.MethodPut, settingsPath+"/group",
		map[string]any{"policy": map[string]any{"concurrency": map[string]any{"max_interactive_requests": 3}}}),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if !strings.Contains(body.Message, "concurrency.max_interactive_requests") {
		t.Errorf("invalid policy message = %q, want the field path", body.Message)
	}
	errorBody(t, h.write(http.MethodPut, settingsPath+"/channel", map[string]any{"policy": map[string]any{}}),
		http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPut, settingsPath+"/group", `{"policy":{},"extra":1}`),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.do(http.MethodPut, settingsPath+"/group", `{"policy":{}}`, withCookie(h.cookie)),
		http.StatusForbidden, conloomapi.CodeForbidden)
	if d := h.policies.Defaults(policy.KindGroup); d.Version != 1 {
		t.Errorf("group defaults at v%d after refused saves, want v1", d.Version)
	}
	if v, err := h.db.ListPolicyDefaults(context.Background(), policy.KindGroup, 0); err != nil || len(v) != 1 {
		t.Errorf("stored group defaults = %d (%v), want 1", len(v), err)
	}
	if a := h.settingsAudit(); len(a) != 1 {
		t.Errorf("settings.policy.update audits = %d, want 1", len(a))
	}
}

func TestSettingsPolicies_PutWarnings(t *testing.T) {
	h := newWriteHarness(t, nil)
	h.cfg.ModelProfiles = map[string]config.ModelProfile{
		"long_context": {Privacy: config.Privacy{AllowSensitiveContent: false}},
	}
	resp := h.save(http.MethodPut, settingsPath+"/private",
		map[string]any{"policy": map[string]any{"memory": map[string]any{"enabled": true}}})
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "敏感内容") {
		t.Errorf("private memory on without sensitive content: warnings %q, want one about 敏感内容", resp.Warnings)
	}

	resp = h.save(http.MethodPut, settingsPath+"/group",
		map[string]any{"policy": map[string]any{"review": map[string]any{"level": "strict"}}})
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "审查级别") || !strings.Contains(resp.Warnings[0], `"strict"`) {
		t.Errorf("group save with an unimplemented review level while private memory lacks sensitive content: warnings %q, want only the one about 审查级别", resp.Warnings)
	}
	resp = h.save(http.MethodPut, settingsPath+"/group", map[string]any{"policy": map[string]any{}})
	if len(resp.Warnings) != 0 {
		t.Errorf("group save while private memory lacks sensitive content: warnings %q, want none", resp.Warnings)
	}
	resp = h.save(http.MethodPut, settingsPath+"/private", map[string]any{"policy": map[string]any{}})
	if len(resp.Warnings) != 0 {
		t.Errorf("private save with memory off: warnings %q, want none", resp.Warnings)
	}
	resp = h.save(http.MethodPost, settingsPath+"/private/restore", map[string]any{"version": 1})
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "敏感内容") {
		t.Errorf("private restore to memory on without sensitive content: warnings %q, want one about 敏感内容", resp.Warnings)
	}

	h.cfg.ModelProfiles = map[string]config.ModelProfile{"long_context": {
		Context: config.ContextLimits{ReservedOutputTokens: 4096}, Privacy: config.Privacy{AllowSensitiveContent: true},
	}}
	resp = h.save(http.MethodPut, settingsPath+"/group", map[string]any{"policy": map[string]any{}})
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "4096") {
		t.Errorf("low output reservation with private memory on: warnings %q, want one about 4096", resp.Warnings)
	}
}

func TestSettingsPolicies_Restore(t *testing.T) {
	h := newWriteHarness(t, nil)
	v1 := h.save(http.MethodPut, settingsPath+"/group",
		map[string]any{"policy": map[string]any{"context": map[string]any{"max_recent_messages": 31}}})
	h.save(http.MethodPut, settingsPath+"/group",
		map[string]any{"policy": map[string]any{"context": map[string]any{"max_recent_messages": 32}}})

	resp := h.save(http.MethodPost, settingsPath+"/group/restore", map[string]any{"version": 1})
	if resp.Current.Version != 3 || !reflect.DeepEqual(resp.Current.Policy, v1.Current.Policy) ||
		resp.Current.Reason != "恢复到第 1 版" || len(resp.Warnings) != 0 {
		t.Errorf("restore v1 = %+v, want v3 equal to v1 with reason 恢复到第 1 版", resp)
	}
	if d := h.policies.Defaults(policy.KindGroup); d.Version != 3 || d.Policy.Context.MaxRecentMessages != 31 {
		t.Errorf("defaults = v%d with %d, want v3 with 31", d.Version, d.Policy.Context.MaxRecentMessages)
	}
	resp = h.save(http.MethodPost, settingsPath+"/group/restore", map[string]any{"version": 2, "reason": "回到第二版"})
	if resp.Current.Version != 4 || resp.Current.Reason != "回到第二版" || resp.Current.Policy.Context.MaxRecentMessages != 32 {
		t.Errorf("restore v2 = %+v", resp.Current)
	}
	if a := h.settingsAudit(); len(a) != 4 || a[0].Action != "restore" || a[0].Version != 4 || a[1].Action != "restore" ||
		a[3].Action != "update" {
		t.Errorf("settings.policy.update audits = %+v, want update, update, restore, restore", a)
	}

	errorBody(t, h.write(http.MethodPost, settingsPath+"/group/restore", map[string]any{"version": 0}),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.write(http.MethodPost, settingsPath+"/group/restore", map[string]any{"version": 99}),
		http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPost, settingsPath+"/private/restore", map[string]any{"version": 1}),
		http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPost, settingsPath+"/channel/restore", map[string]any{"version": 1}),
		http.StatusNotFound, conloomapi.CodeNotFound)
	if d := h.policies.Defaults(policy.KindGroup); d.Version != 4 {
		t.Errorf("defaults at v%d after refused restores, want v4", d.Version)
	}
}
