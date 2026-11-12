package router

import (
	"errors"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
)

func shortProfile() *Profile {
	return &Profile{Role: RoleShort, Config: config.ModelProfile{
		Model:      "short-model",
		Modalities: []string{"text", "image"},
		Context:    config.ContextLimits{NativeWindow: 32768, ConfiguredWindow: 32768, SoftInputBudget: 16000, ReservedOutputTokens: 1024, SafetyMarginTokens: 1024},
		Privacy:    config.Privacy{DataLocation: "remote", AllowSensitiveContent: true},
	}}
}

func longProfile() *Profile {
	return &Profile{Role: RoleLong, Config: config.ModelProfile{
		Model:      "long-model",
		Modalities: []string{"text"},
		Context:    config.ContextLimits{NativeWindow: 1000000, ConfiguredWindow: 1000000, SoftInputBudget: 120000, ReservedOutputTokens: 4096, SafetyMarginTokens: 4096},
		Privacy:    config.Privacy{DataLocation: "remote", AllowSensitiveContent: true},
	}}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newRouter(profiles ...*Profile) (*Router, *clock) {
	c := &clock{t: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	return New(profiles, BreakerConfig{}, c.now), c
}

func mustRoute(t *testing.T, r *Router, req Request) Decision {
	t.Helper()
	d, err := r.Route(req)
	if err != nil {
		t.Fatalf("Route(%+v): %v", req, err)
	}
	return d
}

func TestEffectiveLimits(t *testing.T) {
	c := config.ContextLimits{NativeWindow: 32768, ConfiguredWindow: 16384, ReservedOutputTokens: 1024, SafetyMarginTokens: 1024}
	if w, m := EffectiveLimits(c, 0); w != 16384 || m != 14336 {
		t.Errorf("EffectiveLimits(groupCap 0) = (%d, %d), want (16384, 14336)", w, m)
	}
	if w, m := EffectiveLimits(c, 8000); w != 8000 || m != 5952 {
		t.Errorf("EffectiveLimits(groupCap 8000) = (%d, %d), want (8000, 5952)", w, m)
	}
	c.ConfiguredWindow = 65536
	if w, m := EffectiveLimits(c, 0); w != 32768 || m != 30720 {
		t.Errorf("EffectiveLimits(native smallest) = (%d, %d), want (32768, 30720)", w, m)
	}
}

func TestRoute_ShortWhenFits(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	d := mustRoute(t, r, Request{BaseTokens: 1000, AllowLong: true})
	if d.Profile.Role != RoleShort {
		t.Fatalf("Role = %s, want short", d.Profile.Role)
	}
	if d.PromptBudget != 16000 || d.Reason != "fits_short_context" {
		t.Errorf("Budget = %d, Reason = %q", d.PromptBudget, d.Reason)
	}
	if d.MaxPromptTokens != 30720 || d.MaxOutputTokens != 1024 {
		t.Errorf("MaxPrompt = %d, MaxOutput = %d", d.MaxPromptTokens, d.MaxOutputTokens)
	}
}

func TestRoute_ShortBudgetGrowsToBaseTokens(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	d := mustRoute(t, r, Request{BaseTokens: 20000, AllowLong: true})
	if d.Profile.Role != RoleShort || d.PromptBudget != 20000 {
		t.Errorf("Role = %s, Budget = %d; want short, 20000", d.Profile.Role, d.PromptBudget)
	}
}

func TestRoute_GroupCapLimitsWindow(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	d := mustRoute(t, r, Request{BaseTokens: 1000, GroupMaxContextTokens: 8000, AllowLong: true})
	if d.Profile.Role != RoleShort || d.MaxPromptTokens != 5952 || d.PromptBudget != 5952 {
		t.Errorf("Role = %s, MaxPrompt = %d, Budget = %d; want short, 5952, 5952", d.Profile.Role, d.MaxPromptTokens, d.PromptBudget)
	}
}

func TestRoute_LongWhenExceedsShort(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	d := mustRoute(t, r, Request{BaseTokens: 40000, AllowLong: true})
	if d.Profile.Role != RoleLong {
		t.Fatalf("Role = %s, want long", d.Profile.Role)
	}
	if d.PromptBudget != 120000 || d.Reason != "exceeds_short_fits_long" {
		t.Errorf("Budget = %d, Reason = %q", d.PromptBudget, d.Reason)
	}
	if d.MaxPromptTokens != 991808 || d.MaxOutputTokens != 4096 {
		t.Errorf("MaxPrompt = %d, MaxOutput = %d", d.MaxPromptTokens, d.MaxOutputTokens)
	}
}

func TestRoute_TooLarge(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	if _, err := r.Route(Request{BaseTokens: 2_000_000, AllowLong: true}); !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("err = %v, want ErrContextTooLarge", err)
	}
}

func TestRoute_ImageNeedsImageModality(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	d := mustRoute(t, r, Request{BaseTokens: 1000, NeedsImage: true, AllowLong: true})
	if d.Profile.Role != RoleShort {
		t.Errorf("Role = %s, want short", d.Profile.Role)
	}

	s := shortProfile()
	s.Config.Modalities = []string{"text"}
	r, _ = newRouter(s, longProfile())
	if _, err := r.Route(Request{BaseTokens: 1000, NeedsImage: true, AllowLong: true}); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("err = %v, want ErrNoProfile", err)
	}
}

func TestRoute_SensitiveExcludesNonSensitiveProfile(t *testing.T) {
	s := shortProfile()
	s.Config.Privacy.AllowSensitiveContent = false
	r, _ := newRouter(s, longProfile())

	d := mustRoute(t, r, Request{BaseTokens: 1000, Sensitive: true, AllowLong: true})
	if d.Profile.Role != RoleLong || d.Reason != "short_unavailable_fallback_long" {
		t.Errorf("Role = %s, Reason = %q; want long, short_unavailable_fallback_long", d.Profile.Role, d.Reason)
	}
	d = mustRoute(t, r, Request{BaseTokens: 1000, AllowLong: true})
	if d.Profile.Role != RoleShort {
		t.Errorf("non-sensitive Role = %s, want short", d.Profile.Role)
	}

	l := longProfile()
	l.Config.Privacy.AllowSensitiveContent = false
	r, _ = newRouter(s, l)
	if _, err := r.Route(Request{BaseTokens: 1000, Sensitive: true, AllowLong: true}); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("err = %v, want ErrNoProfile", err)
	}
}

func TestRoute_AllowLongFalse(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	if _, err := r.Route(Request{BaseTokens: 40000}); !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("err = %v, want ErrContextTooLarge", err)
	}
}

func TestRoute_Exclude(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	d := mustRoute(t, r, Request{BaseTokens: 1000, AllowLong: true, Exclude: []Role{RoleShort}})
	if d.Profile.Role != RoleLong || d.Reason != "short_unavailable_fallback_long" {
		t.Errorf("Role = %s, Reason = %q", d.Profile.Role, d.Reason)
	}
	if _, err := r.Route(Request{BaseTokens: 1000, AllowLong: true, Exclude: []Role{RoleShort, RoleLong}}); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("err = %v, want ErrNoProfile", err)
	}
}

func TestRoute_ShortOnly(t *testing.T) {
	r, _ := newRouter(shortProfile())
	if _, err := r.Route(Request{BaseTokens: 40000, AllowLong: true}); !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("err = %v, want ErrContextTooLarge", err)
	}
}

func TestBreaker_OpensAfterThreeFailuresAndFallsBack(t *testing.T) {
	r, c := newRouter(shortProfile(), longProfile())
	req := Request{BaseTokens: 1000, AllowLong: true}

	r.ReportFailure(RoleShort)
	r.ReportFailure(RoleShort)
	if r.BreakerOpen(RoleShort) {
		t.Fatal("breaker open after 2 failures")
	}
	if d := mustRoute(t, r, req); d.Profile.Role != RoleShort {
		t.Fatalf("Role after 2 failures = %s, want short", d.Profile.Role)
	}
	r.ReportFailure(RoleShort)
	if !r.BreakerOpen(RoleShort) {
		t.Fatal("breaker not open after 3 failures")
	}
	d := mustRoute(t, r, req)
	if d.Profile.Role != RoleLong || d.Reason != "short_unavailable_fallback_long" {
		t.Fatalf("Role = %s, Reason = %q; want long, short_unavailable_fallback_long", d.Profile.Role, d.Reason)
	}

	c.t = c.t.Add(31 * time.Second)
	if r.BreakerOpen(RoleShort) {
		t.Fatal("breaker still open after cooldown")
	}
	if d := mustRoute(t, r, req); d.Profile.Role != RoleShort {
		t.Fatalf("Role after cooldown = %s, want short", d.Profile.Role)
	}
}

func TestBreaker_SuccessResetsFailures(t *testing.T) {
	r, _ := newRouter(shortProfile(), longProfile())
	r.ReportFailure(RoleShort)
	r.ReportFailure(RoleShort)
	r.ReportSuccess(RoleShort)
	r.ReportFailure(RoleShort)
	r.ReportFailure(RoleShort)
	if r.BreakerOpen(RoleShort) {
		t.Fatal("breaker open although a success interrupted the failures")
	}
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	r, c := newRouter(shortProfile(), longProfile())
	for range 3 {
		r.ReportFailure(RoleShort)
	}
	c.t = c.t.Add(31 * time.Second)
	r.ReportFailure(RoleShort)
	if !r.BreakerOpen(RoleShort) {
		t.Fatal("breaker not reopened by a half-open failure")
	}
	c.t = c.t.Add(29 * time.Second)
	if !r.BreakerOpen(RoleShort) {
		t.Fatal("reopened breaker closed before a full cooldown")
	}
}

func TestBreaker_CustomConfig(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	r := New([]*Profile{shortProfile(), longProfile()}, BreakerConfig{Failures: 1, Cooldown: 5 * time.Second}, c.now)
	r.ReportFailure(RoleLong)
	if !r.BreakerOpen(RoleLong) {
		t.Fatal("breaker not open after 1 failure with Failures=1")
	}
	c.t = c.t.Add(5 * time.Second)
	if r.BreakerOpen(RoleLong) {
		t.Fatal("breaker still open after 5s cooldown")
	}
}
