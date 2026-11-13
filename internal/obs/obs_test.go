package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
)

func TestMessageAttr_HidesContentByDefault(t *testing.T) {
	text := "你好，world"
	a := MessageAttr(config.ObservabilityConfig{}, text)
	if a.Key != "text_len" {
		t.Fatalf("key = %q, want text_len", a.Key)
	}
	if a.Value.Kind() != slog.KindInt64 || a.Value.Int64() != 8 {
		t.Fatalf("value = %v, want 8 runes", a.Value)
	}

	a = MessageAttr(config.ObservabilityConfig{LogMessageContent: true}, text)
	if a.Key != "text" || a.Value.String() != text {
		t.Fatalf("attr = %v, want text=%q", a, text)
	}
}

func TestNewMetrics_RegistersOnFreshRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.Events.WithLabelValues("accepted").Inc()
	m.Tasks.WithLabelValues("succeeded").Inc()
	m.ModelLatency.WithLabelValues("main", "m1").Observe(0.5)
	m.ModelTokens.WithLabelValues("main", "m1", "prompt").Add(10)
	m.EstimateRatio.WithLabelValues("main").Observe(1.1)
	m.QueueWait.Observe(0.1)
	m.QueueDepth.Set(3)
	m.AdmissionRejects.WithLabelValues("queue_full").Inc()
	m.OutboxDelivered.Inc()
	m.OutboxRedelivered.Inc()
	m.OutboxExpired.Inc()
	m.Review.WithLabelValues("basic", "pass").Inc()
	m.BreakerOpen.WithLabelValues("main").Set(1)
	m.ContinuationExits.WithLabelValues("rule").Inc()
	m.Proactive.WithLabelValues("evaluated").Inc()
	m.MemoryConsolidations.WithLabelValues("ok").Inc()
	m.MemoryConsolidationSeconds.Observe(12)
	m.MemoryAffinityChanges.WithLabelValues("rule").Inc()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := map[string]bool{}
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "conloom_") {
			t.Errorf("metric %q lacks conloom_ prefix", mf.GetName())
		}
		names[mf.GetName()] = true
	}
	for _, name := range []string{"conloom_events_total", "conloom_continuation_exits_total", "conloom_proactive_total"} {
		if !names[name] {
			t.Fatalf("%s missing; got %v", name, names)
		}
	}
	if len(mfs) != 19 {
		t.Errorf("gathered %d metric families, want 19: %v", len(mfs), names)
	}
}

func TestMetrics_MemoryRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.MemoryConsolidations.WithLabelValues("skipped").Inc()
	m.MemoryConsolidationSeconds.Observe(600)
	m.MemoryAffinityChanges.WithLabelValues("consolidate").Inc()
	m.SkillCalls.WithLabelValues("now", "ok").Inc()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	for _, name := range []string{"conloom_memory_consolidations_total", "conloom_memory_consolidation_seconds",
		"conloom_memory_affinity_changes_total", "conloom_skill_calls_total"} {
		if !names[name] {
			t.Errorf("%s missing; got %v", name, names)
		}
	}
}

func TestMetrics_SearchRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.SearchSeconds.WithLabelValues("keyword").Observe(0.07)
	m.FTSBackfillRemaining.Set(1200)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	got := map[string]float64{}
	for _, mf := range mfs {
		for _, mt := range mf.GetMetric() {
			switch {
			case mt.GetHistogram() != nil:
				got[mf.GetName()] = float64(mt.GetHistogram().GetSampleCount())
			case mt.GetGauge() != nil:
				got[mf.GetName()] = mt.GetGauge().GetValue()
			}
		}
	}
	if got["conloom_search_seconds"] != 1 || got["conloom_fts_backfill_remaining"] != 1200 {
		t.Fatalf("search metrics = %v", got)
	}
}

func TestNewLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(config.ObservabilityConfig{LogLevel: "info", LogFormat: "json"}, &buf)
	log.Debug("hidden")
	log.Info("hello", "k", "v")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (debug filtered): %q", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("not JSON: %v: %q", err, lines[0])
	}
	if rec["msg"] != "hello" || rec["k"] != "v" || rec["level"] != "INFO" {
		t.Fatalf("record = %v", rec)
	}
}

func TestNewLogger_TextFormatAndDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(config.ObservabilityConfig{LogLevel: "debug", LogFormat: "text"}, &buf)
	log.Debug("dbg", "k", "v")

	out := buf.String()
	if !strings.Contains(out, "level=DEBUG") || !strings.Contains(out, "msg=dbg") || !strings.Contains(out, "k=v") {
		t.Fatalf("text output = %q", out)
	}
}
