package panel

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// newAutonomyHarness is a writeHarness whose autonomy API is served by a real
// orchestrator over its store, on the harness's clock.
func newAutonomyHarness(t *testing.T) *writeHarness {
	t.Helper()
	return newWriteHarness(t, func(d *Deps) {
		o, err := core.New(core.Deps{
			Store: d.Store.(*sqlite.DB), Router: router.New(nil, router.BreakerConfig{}, time.Now),
			Admission: admission.New(nil), Personas: d.Personas, Policies: d.Policies,
			Reviewer: review.New(nil, review.Options{}), Config: d.Config, Bots: d.Bots, Location: time.UTC, Now: d.Now,
		})
		if err != nil {
			t.Fatalf("core.New: %v", err)
		}
		t.Cleanup(o.Close)
		d.Autonomy = o.Autonomy()
	})
}

func intPtr(v int) *int { return &v }

func TestPanelAutonomy_GetPutDelete(t *testing.T) {
	h := newAutonomyHarness(t)
	ctx := context.Background()
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(private, "在吗", h.now.Add(-time.Hour), false)
	path := convPath(private.Key, "autonomy")
	bar := convPath(private.Key, "autonomy", "buffs", "心情")
	status := convPath(private.Key, "autonomy", "status")

	var st conloomapi.AutonomyState
	h.getJSON(path, &st)
	if st.Status != nil || len(st.Events) != 0 || len(st.Bars) != 2 ||
		st.Bars[0] != (conloomapi.AutonomyBar{Name: "心情", Value: 60, Band: "偏高", Baseline: 60, Description: "高=开心放松，低=低落烦躁"}) ||
		!st.Proactive.Enabled || st.Proactive.Level != conloomapi.PrivateLevelMid {
		t.Fatalf("state = %+v, want the default bars at their baselines", st)
	}

	// Writes need the CSRF token.
	if w := h.do(http.MethodPut, bar, `{"value":20}`, withCookie(h.cookie)); w.Code != http.StatusForbidden {
		t.Errorf("PUT without CSRF: status %d, want 403", w.Code)
	}
	if w := h.do(http.MethodDelete, status, "", withCookie(h.cookie)); w.Code != http.StatusForbidden {
		t.Errorf("DELETE without CSRF: status %d, want 403", w.Code)
	}

	if w := h.write(http.MethodPut, bar, conloomapi.AutonomyBarPut{Value: intPtr(20), Reason: "<b>调低</b>"}); w.Code != http.StatusNoContent {
		t.Fatalf("PUT bar: status %d, body %s", w.Code, w.Body)
	}
	h.getJSON(path, &st)
	if st.Bars[0].Value != 20 || st.Bars[0].Band != "偏低" || len(st.Events) != 1 ||
		st.Events[0] != (conloomapi.AutonomyBarEvent{Name: "心情", Source: store.BuffSourcePanel, Reason: "<b>调低</b>", Before: 60, After: 20, At: h.now}) {
		t.Errorf("state after PUT = %+v", st)
	}
	audits := h.audits("autonomy.buff")
	if len(audits) != 1 || audits[0].Actor != "panel:"+testUser || audits[0].Target != private.Key || audits[0].BotInstanceID != dataBot {
		t.Fatalf("autonomy.buff audits = %+v", audits)
	}

	for _, c := range []struct{ name, path, body string }{
		{"value 101", bar, `{"value":101}`},
		{"value -1", bar, `{"value":-1}`},
		{"no value", bar, `{"reason":"忘了写值"}`},
		{"not an integer", bar, `{"value":20.5}`},
		{"unknown field", bar, `{"value":20,"delta":5}`},
		{"long reason", bar, `{"value":20,"reason":"` + strings.Repeat("长", 41) + `"}`},
		{"unknown bar", convPath(private.Key, "autonomy", "buffs", "饥饿"), `{"value":20}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := errorBody(t, h.write(http.MethodPut, c.path, c.body), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
			if strings.Contains(e.Message, "core:") || strings.Contains(e.Message, "internal") {
				t.Errorf("message = %q", e.Message)
			}
		})
	}
	if e := errorBody(t, h.write(http.MethodPut, convPath(private.Key, "autonomy", "buffs", "饥饿"), `{"value":20}`),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent); !strings.Contains(e.Message, "心情") || !strings.Contains(e.Message, "精力") {
		t.Errorf("unknown bar message %q does not list the bars", e.Message)
	}
	if n := len(h.audits("autonomy.buff")); n != 1 {
		t.Errorf("refused PUTs wrote audits: %d", n)
	}

	// Groups have no hidden state; unknown chats are not found.
	h.getErr(convPath(writeGroup.Key, "autonomy"), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.write(http.MethodPut, convPath(writeGroup.Key, "autonomy", "buffs", "心情"), `{"value":20}`),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.write(http.MethodDelete, convPath(writeGroup.Key, "autonomy", "status"), nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	missing := store.ConversationKey(dataBot, "qq", conloomapi.ConvPrivate, "29999")
	h.getErr(convPath(missing, "autonomy"), http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPut, convPath(missing, "autonomy", "buffs", "心情"), `{"value":20}`),
		http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodDelete, convPath(missing, "autonomy", "status"), nil), http.StatusNotFound, conloomapi.CodeNotFound)

	until := h.now.Add(20 * time.Minute)
	if err := h.db.SetStatus(ctx, store.ConversationStatus{ConversationKey: private.Key, Activity: "<i>洗澡</i>", TaskID: "task_seed",
		StartedAt: h.now.Add(-10 * time.Minute), UntilAt: until},
		&store.ConversationTimer{BotInstanceID: dataBot, PlatformUserID: "20001", DueAt: until}); err != nil {
		t.Fatal(err)
	}
	h.getJSON(path, &st)
	if st.Status == nil || st.Status.Activity != "<i>洗澡</i>" || !st.Status.UntilAt.Equal(until) ||
		len(st.Proactive.Timers) == 0 || st.Proactive.Timers[0].Kind != string(store.TimerStatusReturn) {
		t.Fatalf("state with a status = %+v", st)
	}

	if w := h.write(http.MethodDelete, status, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE status: status %d, body %s", w.Code, w.Body)
	}
	if _, err := h.db.GetStatus(ctx, private.Key); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("status after DELETE err = %v, want ErrNotFound", err)
	}
	if timers, err := h.db.ListTimers(ctx, private.Key); err != nil {
		t.Fatal(err)
	} else {
		for _, tm := range timers {
			if tm.Kind == store.TimerStatusReturn {
				t.Errorf("status return still armed: %+v", timers)
			}
		}
	}
	clears := h.audits("autonomy.status.clear")
	if len(clears) != 1 || clears[0].Actor != "panel:"+testUser || clears[0].Target != private.Key ||
		!strings.Contains(clears[0].Detail, `"cleared":true`) {
		t.Errorf("autonomy.status.clear audits = %+v", clears)
	}
}

func TestPanelAutonomy_Unavailable(t *testing.T) {
	h := newWriteHarness(t, nil)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	errorBody(t, h.get(convPath(private.Key, "autonomy")), http.StatusServiceUnavailable, conloomapi.CodeInternal)
	errorBody(t, h.write(http.MethodPut, convPath(private.Key, "autonomy", "buffs", "心情"), `{"value":20}`),
		http.StatusServiceUnavailable, conloomapi.CodeInternal)
	errorBody(t, h.write(http.MethodDelete, convPath(private.Key, "autonomy", "status"), nil),
		http.StatusServiceUnavailable, conloomapi.CodeInternal)
}
