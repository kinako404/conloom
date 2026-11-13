package panel

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

type liveBody struct {
	Metrics []struct {
		Name   string                       `json:"name"`
		Help   string                       `json:"help"`
		Type   string                       `json:"type"`
		Series []map[string]json.RawMessage `json:"series"`
	} `json:"metrics"`
}

func floatNear(raw json.RawMessage, want float64) bool {
	var v float64
	return json.Unmarshal(raw, &v) == nil && math.Abs(v-want) < 1e-9
}

func TestMetricsLive_HistogramPercentiles(t *testing.T) {
	h := newOverviewHarness(t, nil)
	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "conloom_test_seconds", Help: "Test latency.", Buckets: []float64{1, 2, 4},
	}, []string{"role"})
	calls := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "conloom_test_total", Help: "Test calls."}, []string{"result"})
	depth := prometheus.NewGauge(prometheus.GaugeOpts{Name: "conloom_test_depth", Help: "Test depth."})
	summary := prometheus.NewSummary(prometheus.SummaryOpts{Name: "conloom_test_summary", Help: "Not shown."})
	h.gatherer.MustRegister(latency, calls, depth, summary)
	for _, v := range []float64{0.5, 1.5, 1.5, 3} {
		latency.WithLabelValues("a").Observe(v)
	}
	latency.WithLabelValues("b").Observe(10)
	latency.WithLabelValues("c").Observe(0.5)
	latency.WithLabelValues("idle")
	calls.WithLabelValues("ok").Add(3)
	depth.Set(7)
	summary.Observe(1)

	w := h.get("/panel/api/metrics/live")
	var body liveBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	var names []string
	for _, m := range body.Metrics {
		names = append(names, m.Name+":"+m.Type)
	}
	if got := strings.Join(names, " "); got != "conloom_test_depth:gauge conloom_test_seconds:histogram conloom_test_total:counter" {
		t.Fatalf("metrics = %s", got)
	}

	depthM, hist, counter := body.Metrics[0], body.Metrics[1], body.Metrics[2]
	if depthM.Help != "Test depth." || len(depthM.Series) != 1 || string(depthM.Series[0]["labels"]) != "{}" ||
		!floatNear(depthM.Series[0]["value"], 7) || len(depthM.Series[0]) != 2 {
		t.Errorf("gauge = %+v", depthM)
	}
	if len(counter.Series) != 1 || string(counter.Series[0]["labels"]) != `{"result":"ok"}` || !floatNear(counter.Series[0]["value"], 3) {
		t.Errorf("counter = %+v", counter)
	}

	if len(hist.Series) != 4 {
		t.Fatalf("histogram series = %d, want 4", len(hist.Series))
	}
	for i, want := range []struct {
		labels        string
		count         string
		sum, p50, p95 float64
	}{
		{`{"role":"a"}`, "4", 6.5, 1.5, 3.6},
		{`{"role":"b"}`, "1", 10, 4, 4},
		{`{"role":"c"}`, "1", 0.5, 0.5, 0.95},
	} {
		s := hist.Series[i]
		if string(s["labels"]) != want.labels || string(s["count"]) != want.count || !floatNear(s["sum"], want.sum) ||
			!floatNear(s["p50"], want.p50) || !floatNear(s["p95"], want.p95) || len(s) != 5 {
			t.Errorf("series %d = %s %s %s %s %s, want %+v", i, s["labels"], s["count"], s["sum"], s["p50"], s["p95"], want)
		}
	}
	idle := hist.Series[3]
	if string(idle["labels"]) != `{"role":"idle"}` || string(idle["count"]) != "0" || string(idle["p50"]) != "null" ||
		string(idle["p95"]) != "null" {
		t.Errorf("series without observations = %v, want count 0 and null percentiles", idle)
	}
}

func TestMetricsLive_NoGatherer(t *testing.T) {
	h := newOverviewHarness(t, func(d *Deps) { d.Gatherer = nil })
	w := h.get("/panel/api/metrics/live")
	if w.Code != 200 || w.Body.String() != "{\"metrics\":[]}\n" {
		t.Errorf("no gatherer: %d %s", w.Code, w.Body)
	}
}
