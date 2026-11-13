package panel

import (
	"math"
	"net/http"

	dto "github.com/prometheus/client_model/go"
)

type liveMetric struct {
	Name   string `json:"name"`
	Help   string `json:"help"`
	Type   string `json:"type"`
	Series []any  `json:"series"` // valueSeries or histogramSeries
}

// valueSeries is a counter or gauge series. Values that JSON cannot hold
// (NaN, ±Inf) are null.
type valueSeries struct {
	Labels map[string]string `json:"labels"`
	Value  *float64          `json:"value"`
}

// histogramSeries is a histogram series; P50 and P95 are estimated from the
// buckets and null without observations.
type histogramSeries struct {
	Labels map[string]string `json:"labels"`
	Count  uint64            `json:"count"`
	Sum    *float64          `json:"sum"`
	P50    *float64          `json:"p50"`
	P95    *float64          `json:"p95"`
}

var metricTypes = map[dto.MetricType]string{
	dto.MetricType_COUNTER:   "counter",
	dto.MetricType_GAUGE:     "gauge",
	dto.MetricType_HISTOGRAM: "histogram",
}

// metricsLive serves a snapshot of the counters, gauges and histograms in
// the registry; other metric types are left out.
func (s *Server) metricsLive(w http.ResponseWriter, r *http.Request, _ *Session) {
	out := []liveMetric{}
	for _, mf := range s.gather(r) {
		typ, ok := metricTypes[mf.GetType()]
		if !ok {
			continue
		}
		lm := liveMetric{Name: mf.GetName(), Help: mf.GetHelp(), Type: typ, Series: make([]any, 0, len(mf.GetMetric()))}
		for _, m := range mf.GetMetric() {
			labels := make(map[string]string, len(m.GetLabel()))
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				lm.Series = append(lm.Series, valueSeries{Labels: labels, Value: finite(m.GetCounter().GetValue())})
			case dto.MetricType_GAUGE:
				lm.Series = append(lm.Series, valueSeries{Labels: labels, Value: finite(m.GetGauge().GetValue())})
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				lm.Series = append(lm.Series, histogramSeries{
					Labels: labels, Count: h.GetSampleCount(), Sum: finite(h.GetSampleSum()),
					P50: bucketQuantile(0.5, h), P95: bucketQuantile(0.95, h),
				})
			}
		}
		out = append(out, lm)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"metrics": out})
}

// gather collects the registry's metric families, sorted by name; a nil
// Gatherer has none. When collection partly fails the failure is logged and
// the families collected are used.
func (s *Server) gather(r *http.Request) []*dto.MetricFamily {
	if s.d.Gatherer == nil {
		return nil
	}
	mfs, err := s.d.Gatherer.Gather()
	if err != nil {
		s.log.Warn("gather metrics failed", "trace_id", traceID(r.Context()), "error", err)
	}
	return mfs
}

// family returns the family named name, nil when mfs has none.
func family(mfs []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf
		}
	}
	return nil
}

// gaugeCount returns the unlabelled gauge name as a count, nil when mfs has
// no such gauge.
func gaugeCount(mfs []*dto.MetricFamily, name string) *int64 {
	mf := family(mfs, name)
	if mf.GetType() != dto.MetricType_GAUGE || len(mf.GetMetric()) == 0 {
		return nil
	}
	n := int64(mf.GetMetric()[0].GetGauge().GetValue())
	return &n
}

// bucketQuantile estimates the q-quantile of h's observations as Prometheus's
// histogram_quantile does: it finds the bucket holding the observation of
// rank q*count and interpolates linearly within it, the first bucket starting
// at 0. A rank above the highest bucket yields that bucket's upper bound. It
// is nil when h has no observations or no finite buckets.
func bucketQuantile(q float64, h *dto.Histogram) *float64 {
	count := h.GetSampleCount()
	if count == 0 {
		return nil
	}
	rank := q * float64(count)
	var highest *float64
	lower, below := 0.0, uint64(0)
	for _, b := range h.GetBucket() {
		upper, cum := b.GetUpperBound(), b.GetCumulativeCount()
		if math.IsInf(upper, 1) {
			break
		}
		lower = min(lower, upper)
		if float64(cum) >= rank {
			v := lower + (upper-lower)*(rank-float64(below))/float64(cum-below)
			return &v
		}
		lower, below, highest = upper, cum, &upper
	}
	return highest
}

// finite returns &v, or nil when v is NaN or infinite.
func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}
