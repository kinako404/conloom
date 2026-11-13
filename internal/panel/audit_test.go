package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestAudit_List(t *testing.T) {
	dh := newDataHarness(t)
	ctx := context.Background()

	if body := string(dh.getJSON("/panel/api/audit", nil)); body != "{\"events\":[],\"next_before\":null}\n" {
		t.Errorf("no events: body %q", body)
	}

	at := time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)
	for _, e := range []store.AuditEvent{
		{ID: "aud_01", BotInstanceID: dataBot, Kind: "policy.update", Actor: "panel:admin", Target: "qq-main:qq:group:10001",
			Detail: `{"action":"pause","version":2}`, CreatedAt: at},
		{ID: "aud_02", BotInstanceID: "tg-main", Kind: "policy.update", Actor: "api", Target: "tg-main:telegram:group:-1001234",
			Detail: `not json <script>alert(1)</script>`, CreatedAt: at.Add(time.Minute)},
		{ID: "aud_03", Kind: "panel.login", Actor: "panel:admin", Detail: ``, CreatedAt: at.Add(2 * time.Minute)},
	} {
		if err := dh.db.InsertAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	type event struct {
		ID            string          `json:"id"`
		BotInstanceID string          `json:"bot_instance_id"`
		Kind          string          `json:"kind"`
		Actor         string          `json:"actor"`
		Target        string          `json:"target"`
		Detail        json.RawMessage `json:"detail"`
		CreatedAt     time.Time       `json:"created_at"`
	}
	var page struct {
		Events     []event `json:"events"`
		NextBefore *string `json:"next_before"`
	}
	ids := func() string {
		var out []string
		for _, e := range page.Events {
			out = append(out, e.ID)
		}
		return fmt.Sprint(out)
	}

	dh.getJSON("/panel/api/audit?limit=2", &page)
	if ids() != "[aud_03 aud_02]" || page.NextBefore == nil || *page.NextBefore != "aud_02" {
		t.Fatalf("page 1 = %s next %v, want [aud_03 aud_02] next aud_02", ids(), page.NextBefore)
	}
	if d := string(page.Events[1].Detail); d != `"not json \u003cscript\u003ealert(1)\u003c/script\u003e"` {
		t.Errorf("non-JSON detail = %s, want it as a string", d)
	}
	if d := string(page.Events[0].Detail); d != `""` {
		t.Errorf("empty detail = %s, want an empty string", d)
	}
	dh.getJSON("/panel/api/audit?limit=2&before=aud_02", &page)
	if ids() != "[aud_01]" || page.NextBefore != nil {
		t.Fatalf("page 2 = %s next %v, want [aud_01] and no next", ids(), page.NextBefore)
	}
	e := page.Events[0]
	if e.BotInstanceID != dataBot || e.Kind != "policy.update" || e.Actor != "panel:admin" ||
		e.Target != "qq-main:qq:group:10001" || !e.CreatedAt.Equal(at) {
		t.Errorf("aud_01 = %+v", e)
	}
	var detail map[string]any
	if err := json.Unmarshal(e.Detail, &detail); err != nil || detail["action"] != "pause" || detail["version"] != float64(2) {
		t.Errorf("JSON detail = %s, want it as an object", e.Detail)
	}

	dh.getJSON("/panel/api/audit?kind=policy.update&bot=tg-main", &page)
	if ids() != "[aud_02]" {
		t.Errorf("kind and bot filter = %s, want [aud_02]", ids())
	}
	dh.getErr("/panel/api/audit?limit=x", http.StatusBadRequest, conloomapi.CodeInvalidEvent)
}
