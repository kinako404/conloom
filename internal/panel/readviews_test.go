package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/gateway"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type personaVersionResp struct {
	Version   int                   `json:"version"`
	Template  string                `json:"template"`
	Persona   conloomapi.PersonaDoc `json:"persona"`
	Reason    string                `json:"reason"`
	UpdatedBy string                `json:"updated_by"`
	CreatedAt time.Time             `json:"created_at"`
}

type personaResp struct {
	Current         *personaVersionResp    `json:"current"`
	PendingTemplate *string                `json:"pending_template"`
	TemplateDoc     *conloomapi.PersonaDoc `json:"template_doc"`
	Versions        []personaVersionResp   `json:"versions"`
	Templates       []string               `json:"templates"`
}

func TestPersona_ReadOnlyWhenMissing(t *testing.T) {
	dh := newDataHarness(t)
	ctx := context.Background()
	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")

	var got personaResp
	raw := dh.getJSON(convPath(group.Key, "persona"), &got)
	if n, err := dh.db.ListPersonaVersions(ctx, group.Key); err != nil || len(n) != 0 {
		t.Fatalf("GET persona wrote versions: %v, %v", n, err)
	}
	if got.Current != nil || !bytes.Contains(raw, []byte(`"current":null`)) {
		t.Errorf("current = %+v, want null (body %s)", got.Current, raw)
	}
	if got.PendingTemplate == nil || *got.PendingTemplate != "lighthouse" {
		t.Errorf("pending_template = %v, want lighthouse", got.PendingTemplate)
	}
	if got.TemplateDoc == nil || !reflect.DeepEqual(*got.TemplateDoc, lighthousePersona()) {
		t.Errorf("template_doc = %+v, want the lighthouse template", got.TemplateDoc)
	}
	if got.Versions == nil || len(got.Versions) != 0 || !reflect.DeepEqual(got.Templates, []string{"default", "lighthouse"}) {
		t.Errorf("versions = %v, templates = %v; want [] and [default lighthouse]", got.Versions, got.Templates)
	}

	dh.cfg.Bots[0].PersonaTemplate = ""
	dh.getJSON(convPath(group.Key, "persona"), &got)
	if got.PendingTemplate == nil || *got.PendingTemplate != "default" ||
		got.TemplateDoc == nil || !reflect.DeepEqual(*got.TemplateDoc, conloomapi.DefaultPersona()) {
		t.Errorf("bot without a template: pending %v doc %+v, want default", got.PendingTemplate, got.TemplateDoc)
	}
	if n, err := dh.db.ListPersonaVersions(ctx, group.Key); err != nil || len(n) != 0 {
		t.Fatalf("GET persona wrote versions: %v, %v", n, err)
	}
}

func TestPersona_CurrentAndVersions(t *testing.T) {
	dh := newDataHarness(t)
	ctx := context.Background()
	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")
	if _, err := dh.personas.Update(ctx, group.Key, "lighthouse", conloomapi.PersonaPut{Template: "default", Reason: "初始"}, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	custom := lighthousePersona()
	custom.Description = "<img src=x onerror=alert(1)>"
	if _, err := dh.personas.Update(ctx, group.Key, "lighthouse", conloomapi.PersonaPut{Persona: &custom, Reason: "改描述"}, "panel:admin"); err != nil {
		t.Fatal(err)
	}

	var got personaResp
	raw := dh.getJSON(convPath(group.Key, "persona"), &got)
	if got.Current == nil || got.Current.Version != 2 || got.Current.Template != "lighthouse" ||
		!reflect.DeepEqual(got.Current.Persona, custom) || got.Current.Reason != "改描述" ||
		got.Current.UpdatedBy != "panel:admin" || got.Current.CreatedAt.IsZero() {
		t.Errorf("current = %+v", got.Current)
	}
	if got.PendingTemplate != nil || got.TemplateDoc != nil ||
		!bytes.Contains(raw, []byte(`"pending_template":null`)) || !bytes.Contains(raw, []byte(`"template_doc":null`)) {
		t.Errorf("pending_template/template_doc = %v/%v, want null (body %s)", got.PendingTemplate, got.TemplateDoc, raw)
	}
	if len(got.Versions) != 2 || got.Versions[0].Version != 2 || got.Versions[1].Version != 1 ||
		got.Versions[1].Template != "default" || got.Versions[1].Reason != "初始" {
		t.Errorf("versions = %+v, want v2 then v1 (default)", got.Versions)
	}
}

type policyVersionResp struct {
	Version   int                    `json:"version"`
	Policy    conloomapi.GroupPolicy `json:"policy"`
	Reason    string                 `json:"reason"`
	UpdatedBy string                 `json:"updated_by"`
	CreatedAt *time.Time             `json:"created_at"`
}

type policyResp struct {
	Current  policyVersionResp   `json:"current"`
	Versions []policyVersionResp `json:"versions"`
}

func TestPolicy_CurrentAndVersions(t *testing.T) {
	dh := newDataHarness(t)
	ctx := context.Background()
	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")

	var got policyResp
	raw := dh.getJSON(convPath(group.Key, "policy"), &got)
	if got.Current.Version != 0 || !reflect.DeepEqual(got.Current.Policy, conloomapi.DefaultGroupPolicy()) ||
		got.Current.CreatedAt != nil || got.Versions == nil || len(got.Versions) != 0 {
		t.Errorf("no stored policy: %s, want the defaults as version 0 with created_at null and no versions", raw)
	}
	if n, err := dh.db.ListPolicyVersions(ctx, group.Key); err != nil || len(n) != 0 {
		t.Fatalf("GET policy wrote versions: %v, %v", n, err)
	}

	p := conloomapi.DefaultGroupPolicy()
	p.Context.MaxRecentMessages = 30
	if _, err := dh.policies.Update(ctx, group.Key, conloomapi.PolicyPut{Policy: p, Reason: "少带点历史"}, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := dh.policies.Pause(ctx, group.Key, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	dh.getJSON(convPath(group.Key, "policy"), &got)
	if got.Current.Version != 2 || got.Current.Policy.Concurrency.MaxInteractiveRequests != 0 ||
		got.Current.Policy.Context.MaxRecentMessages != 30 || got.Current.Reason != "pause" ||
		got.Current.UpdatedBy != "panel:admin" || got.Current.CreatedAt == nil {
		t.Errorf("current = %+v", got.Current)
	}
	if len(got.Versions) != 2 || got.Versions[0].Version != 2 || got.Versions[1].Version != 1 ||
		got.Versions[1].Reason != "少带点历史" || got.Versions[1].Policy.Concurrency.MaxInteractiveRequests != 1 {
		t.Errorf("versions = %+v, want v2 then v1", got.Versions)
	}
}

func TestMemory_ViewMatchesAPI(t *testing.T) {
	dh := newDataHarness(t)
	ctx := context.Background()
	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")
	dh.seedMessage(group, "我喜欢爬山", time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), false)
	if err := dh.db.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: group.Key, Summary: "群里常聊爬山", CursorSeq: 1,
		LastRunAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	note, affinity := "喜欢爬山", 60
	if _, err := dh.memAdmin.EditMember(ctx, group, "20001", conloomapi.MemberMemoryPut{Note: &note, Affinity: &affinity},
		store.RevisionPanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}

	const apiToken = "test-memory-token"
	gw := gateway.New(gateway.Deps{
		Config: &config.Config{APITokens: []config.APIToken{{
			Name: "reader", Token: apiToken, BotInstanceID: dataBot, Scopes: []string{conloomapi.ScopeMemoryRead},
		}}},
		Store: dh.db, Policies: dh.policies, MemoryAdmin: dh.memAdmin,
	})
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/api/v1/conversations/"+group.ID+"/memory", nil)
	r.Header.Set("Authorization", "Bearer "+apiToken)
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("API memory: status %d, body %s", w.Code, w.Body)
	}

	panelBody := dh.getJSON(convPath(group.Key, "memory"), nil)
	if !bytes.Equal(panelBody, w.Body.Bytes()) {
		t.Errorf("panel memory\n%s\ndiffers from API memory\n%s", panelBody, w.Body)
	}
	var state conloomapi.MemoryState
	if err := json.Unmarshal(panelBody, &state); err != nil {
		t.Fatal(err)
	}
	if state.Summary != "群里常聊爬山" || len(state.Members) != 1 || state.Members[0].Note != note {
		t.Errorf("memory = %+v", state)
	}
}

func TestMemory_Revisions(t *testing.T) {
	dh := newDataHarness(t)
	ctx := context.Background()
	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")
	note := "喜欢爬山"
	if _, err := dh.memAdmin.EditMember(ctx, group, "20001", conloomapi.MemberMemoryPut{Note: &note, Reason: "补充"},
		store.RevisionPanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	note = "喜欢徒步"
	if _, err := dh.memAdmin.EditMember(ctx, group, "20001", conloomapi.MemberMemoryPut{Note: &note},
		store.RevisionPanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}

	type revision struct {
		ID        int64     `json:"id"`
		UserID    string    `json:"user_id"`
		Field     string    `json:"field"`
		Old       string    `json:"old"`
		New       string    `json:"new"`
		Source    string    `json:"source"`
		Reason    string    `json:"reason"`
		CreatedAt time.Time `json:"created_at"`
	}
	var got struct {
		Revisions []revision `json:"revisions"`
	}
	dh.getJSON(convPath(group.Key, "memory", "revisions")+"?user_id=20001", &got)
	if len(got.Revisions) != 2 {
		t.Fatalf("revisions = %+v, want 2", got.Revisions)
	}
	first, second := got.Revisions[0], got.Revisions[1]
	if first.ID == 0 || first.UserID != "20001" || first.Field != store.FieldNote || first.Old != "" || first.New != "喜欢爬山" ||
		first.Source != store.RevisionPanel || first.Reason != "补充" || first.CreatedAt.IsZero() {
		t.Errorf("first revision = %+v", first)
	}
	if second.Old != "喜欢爬山" || second.New != "喜欢徒步" {
		t.Errorf("second revision = %+v", second)
	}

	if body := string(dh.getJSON(convPath(group.Key, "memory", "revisions"), nil)); body != "{\"revisions\":[]}\n" {
		t.Errorf("summary revisions: body %q, want none", body)
	}
	dh.getErr(convPath("qq-main:qq:group:missing", "memory", "revisions"), http.StatusNotFound, conloomapi.CodeNotFound)
}
