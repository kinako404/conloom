package app_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
)

func TestE2E_BreakerGaugeFollowsTheCooldown(t *testing.T) {
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return 503, "upstream down" })
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	reg := prometheus.NewRegistry()
	e := newEnvWith(t, testConfig(t, configParams{modelURL: fake.URL}), app.Options{Registry: reg, Now: now})

	if v, ok := gaugeValue(t, reg, "conloom_breaker_open", "short_context"); !ok || v != 0 {
		t.Fatalf("breaker gauge before any call = %v (present %v), want 0", v, ok)
	}
	for _, text := range []string{"在吗？", "还在吗？", "人呢？"} {
		e.mention(group, alice, text)
		e.drainOne()
	}
	if n := fake.Calls(); n != 3 {
		t.Fatalf("model calls = %d, want 3", n)
	}
	if v, _ := gaugeValue(t, reg, "conloom_breaker_open", "short_context"); v != 1 {
		t.Fatalf("breaker gauge after 3 failures = %v, want 1", v)
	}

	offset.Store(int64(31 * time.Second))
	if v, _ := gaugeValue(t, reg, "conloom_breaker_open", "short_context"); v != 0 {
		t.Fatalf("breaker gauge after the 30s cooldown = %v, want 0 without another model call", v)
	}
}

// gaugeValue returns the value of the gauge name with a label of value
// labelValue, and whether it is there.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name, labelValue string) (float64, bool) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() == labelValue {
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}
