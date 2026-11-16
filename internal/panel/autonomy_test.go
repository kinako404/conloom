package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
		st.Events[0] != (conloomapi.AutonomyBarEvent{Name: "心情", Kind: store.BuffKindAdjust, Source: store.BuffSourcePanel, Reason: "<b>调低</b>",
			Before: 60, After: 20, At: h.now}) {
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
	errorBody(t, h.write(http.MethodDelete, convPath(private.Key, "autonomy", "bars", "饥饿"), nil),
		http.StatusServiceUnavailable, conloomapi.CodeInternal)
	errorBody(t, h.write(http.MethodDelete, convPath(private.Key, "autonomy", "effects", "着凉"), nil),
		http.StatusServiceUnavailable, conloomapi.CodeInternal)
}

// seedBotBuffs gives the private chat key the bars 精力 (since shadowed by
// the persona's 精力), 饥饿 and 饱/饿 and the effect 着凉, as the bot added
// them.
func (h *writeHarness) seedBotBuffs(key string) {
	h.t.Helper()
	ctx := context.Background()
	for i, b := range []store.CustomBar{
		{Name: "精力", Description: "自建的精力", Baseline: 10, HalfLife: time.Hour},
		{Name: "饥饿", Description: "<i>高=很饿</i>", Baseline: 30, HalfLife: 6 * time.Hour},
		{Name: "饱/饿", Baseline: 50, HalfLife: 6 * time.Hour},
	} {
		b.ConversationKey, b.TaskID, b.CreatedAt = key, fmt.Sprintf("task_%d", i), h.now.Add(time.Duration(i-3)*time.Hour)
		if err := h.db.AddCustomBar(ctx, b, float64(b.Baseline), 6); err != nil {
			h.t.Fatal(err)
		}
	}
	started := h.now.Add(-10 * time.Minute)
	if err := h.db.AddEffect(ctx, store.Effect{ConversationKey: key, Name: "着凉", Description: "<b>有点冷</b>", TaskID: "task_9",
		StartedAt: started, UntilAt: h.now.Add(time.Hour)}, 6, started); err != nil {
		h.t.Fatal(err)
	}
}

func TestPanelAutonomy_RemoveCustomBar(t *testing.T) {
	h := newAutonomyHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(private, "在吗", h.now.Add(-time.Hour), false)
	h.seedBotBuffs(private.Key)
	path := convPath(private.Key, "autonomy")
	bar := func(name string) string { return convPath(private.Key, "autonomy", "bars", url.PathEscape(name)) }

	raw := h.get(path)
	if raw.Code != http.StatusOK || !strings.Contains(raw.Body.String(), `"custom":false`) {
		t.Fatalf("GET: status %d, body %s; want custom on every bar", raw.Code, raw.Body)
	}
	var st conloomapi.AutonomyState
	h.getJSON(path, &st)
	var names []string
	for _, b := range st.Bars {
		names = append(names, fmt.Sprintf("%s:%v", b.Name, b.Custom))
	}
	if got := strings.Join(names, " "); got != "心情:false 精力:false 饥饿:true 饱/饿:true" {
		t.Fatalf("bars = %s, want the persona's then the bot's, without its shadowed 精力", got)
	}
	if st.Bars[2] != (conloomapi.AutonomyBar{Name: "饥饿", Value: 30, Band: "偏低", Baseline: 30, Description: "<i>高=很饿</i>", Custom: true}) {
		t.Errorf("饥饿 = %+v", st.Bars[2])
	}
	if len(st.ShadowedBars) != 1 || st.ShadowedBars[0] != "精力" {
		t.Errorf("shadowed bars = %q, want the bot's 精力", st.ShadowedBars)
	}

	if w := h.do(http.MethodDelete, bar("饥饿"), "", withCookie(h.cookie)); w.Code != http.StatusForbidden {
		t.Errorf("DELETE without CSRF: status %d, want 403", w.Code)
	}
	if e := errorBody(t, h.write(http.MethodDelete, bar("心情"), nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent); !strings.Contains(e.Message, "心情") ||
		strings.Contains(e.Message, "core:") {
		t.Errorf("DELETE of the persona's bar: message %q", e.Message)
	}
	errorBody(t, h.write(http.MethodDelete, bar("口渴"), nil), http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodDelete, bar("着凉"), nil), http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodDelete, convPath(writeGroup.Key, "autonomy", "bars", "饥饿"), nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	missing := store.ConversationKey(dataBot, "qq", conloomapi.ConvPrivate, "29999")
	errorBody(t, h.write(http.MethodDelete, convPath(missing, "autonomy", "bars", "饥饿"), nil), http.StatusNotFound, conloomapi.CodeNotFound)
	if n := len(h.audits("autonomy.bar.remove")); n != 0 {
		t.Fatalf("refused DELETEs wrote %d audits", n)
	}

	for _, name := range []string{"饥饿", "饱/饿"} {
		if w := h.write(http.MethodDelete, bar(name), nil); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s: status %d, body %s", name, w.Code, w.Body)
		}
	}
	h.getJSON(path, &st)
	if len(st.Bars) != 2 || st.Bars[1].Name != "精力" || st.Bars[1].Custom ||
		st.Events[0] != (conloomapi.AutonomyBarEvent{Name: "饱/饿", Kind: store.BuffKindBarRemove, Source: store.BuffSourcePanel,
			Before: 50, After: 50, At: h.now}) {
		t.Fatalf("state after DELETE = %+v", st)
	}

	// The bot's own 精力, shadowed by the persona's, can go; then 精力 is the
	// persona's alone.
	before := st.Bars[1]
	if w := h.write(http.MethodDelete, bar("精力"), nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE the bot's shadowed 精力: status %d, body %s", w.Code, w.Body)
	}
	if custom, err := h.db.ListCustomBars(context.Background(), private.Key); err != nil || len(custom) != 0 {
		t.Errorf("bot's bars after DELETE = %+v, %v; want none", custom, err)
	}
	if raw := h.get(path); !strings.Contains(raw.Body.String(), `"shadowed_bars":[]`) {
		t.Errorf("GET after removing the bot's 精力 = %s, want no shadowed bars", raw.Body)
	}
	h.getJSON(path, &st)
	if st.Bars[1] != before {
		t.Errorf("persona's 精力 = %+v after removing the bot's, want %+v", st.Bars[1], before)
	}
	errorBody(t, h.write(http.MethodDelete, bar("精力"), nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)

	audits := h.audits("autonomy.bar.remove")
	if len(audits) != 3 {
		t.Fatalf("autonomy.bar.remove audits = %+v, want 3", audits)
	}
	for _, a := range audits {
		if a.Actor != "panel:"+testUser || a.Target != private.Key || a.BotInstanceID != dataBot || !strings.HasPrefix(a.Detail, `{"name":"`) ||
			strings.Contains(a.Detail, ",") {
			t.Errorf("audit = %+v, want the panel user's with the name only", a)
		}
	}
}

func TestPanelAutonomy_RemoveEffect(t *testing.T) {
	h := newAutonomyHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(private, "在吗", h.now.Add(-time.Hour), false)
	h.seedBotBuffs(private.Key)
	path := convPath(private.Key, "autonomy")
	effect := convPath(private.Key, "autonomy", "effects", "着凉")

	var st conloomapi.AutonomyState
	h.getJSON(path, &st)
	if len(st.Effects) != 1 || st.Effects[0] != (conloomapi.AutonomyEffect{Name: "着凉", Description: "<b>有点冷</b>",
		StartedAt: h.now.Add(-10 * time.Minute), UntilAt: h.now.Add(time.Hour)}) ||
		st.Events[0].Kind != store.BuffKindEffectAdd || st.Events[0].Name != "着凉" {
		t.Fatalf("state = %+v, want the effect and its addition", st)
	}

	if w := h.do(http.MethodDelete, effect, "", withCookie(h.cookie)); w.Code != http.StatusForbidden {
		t.Errorf("DELETE without CSRF: status %d, want 403", w.Code)
	}
	errorBody(t, h.write(http.MethodDelete, convPath(private.Key, "autonomy", "effects", "饥饿"), nil), http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodDelete, convPath(writeGroup.Key, "autonomy", "effects", "着凉"), nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	missing := store.ConversationKey(dataBot, "qq", conloomapi.ConvPrivate, "29999")
	errorBody(t, h.write(http.MethodDelete, convPath(missing, "autonomy", "effects", "着凉"), nil), http.StatusNotFound, conloomapi.CodeNotFound)

	if w := h.write(http.MethodDelete, effect, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d, body %s", w.Code, w.Body)
	}
	h.getJSON(path, &st)
	if len(st.Effects) != 0 || st.Events[0] != (conloomapi.AutonomyBarEvent{Name: "着凉", Kind: store.BuffKindEffectRemove,
		Source: store.BuffSourcePanel, At: h.now}) {
		t.Fatalf("state after DELETE = %+v", st)
	}
	errorBody(t, h.write(http.MethodDelete, effect, nil), http.StatusNotFound, conloomapi.CodeNotFound)

	audits := h.audits("autonomy.effect.remove")
	if len(audits) != 1 || audits[0].Actor != "panel:"+testUser || audits[0].Target != private.Key || audits[0].BotInstanceID != dataBot ||
		audits[0].Detail != `{"name":"着凉"}` {
		t.Errorf("autonomy.effect.remove audits = %+v", audits)
	}
}
