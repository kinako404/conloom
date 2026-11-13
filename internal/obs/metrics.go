package obs

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Events            *prometheus.CounterVec   // disposition
	Tasks             *prometheus.CounterVec   // status
	ModelLatency      *prometheus.HistogramVec // role, model
	ModelTokens       *prometheus.CounterVec   // role, model, kind: prompt|completion|reasoning
	EstimateRatio     *prometheus.HistogramVec // role: actual prompt tokens / estimated
	QueueWait         prometheus.Histogram
	QueueDepth        prometheus.Gauge
	AdmissionRejects  *prometheus.CounterVec // reason
	OutboxDelivered   prometheus.Counter
	OutboxRedelivered prometheus.Counter
	OutboxExpired     prometheus.Counter
	Review            *prometheus.CounterVec // stage, verdict
	BreakerOpen       *prometheus.GaugeVec   // role
	ContinuationExits *prometheus.CounterVec // reason: rule|model|limit|idle
	Proactive         *prometheus.CounterVec // result: evaluated|sent|skipped

	MemoryConsolidations       *prometheus.CounterVec // result: ok|error|skipped
	MemoryConsolidationSeconds prometheus.Histogram
	MemoryAffinityChanges      *prometheus.CounterVec // source

	SkillCalls *prometheus.CounterVec // tool, result: ok|error|unknown|invalid

	CleanupDeleted       *prometheus.CounterVec   // table
	SearchSeconds        *prometheus.HistogramVec // path: keyword|vector
	FTSBackfillRemaining prometheus.Gauge
	EmbeddingRequests    *prometheus.CounterVec // result: ok|error|timeout
	EmbeddingBacklog     prometheus.Gauge

	UsageDropped       prometheus.Counter
	UsageWriteErrors   prometheus.Counter
	PanelLoginFailures prometheus.Counter
	PanelRequests      *prometheus.CounterVec // route, code
}

// NewMetrics creates all ConLoom metrics and registers them on reg; it panics
// if registration fails (e.g. registering twice on the same registry).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_events_total", Help: "Inbound events by disposition.",
		}, []string{"disposition"}),
		Tasks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_tasks_total", Help: "Tasks reaching a status.",
		}, []string{"status"}),
		ModelLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "conloom_model_latency_seconds", Help: "Model call latency.",
			Buckets: []float64{0.25, 0.5, 1, 2, 4, 8, 15, 30, 60, 120},
		}, []string{"role", "model"}),
		ModelTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_model_tokens_total", Help: "Model tokens by kind (prompt|completion|reasoning).",
		}, []string{"role", "model", "kind"}),
		EstimateRatio: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "conloom_token_estimate_ratio", Help: "Actual prompt tokens divided by estimated prompt tokens.",
			Buckets: []float64{0.5, 0.75, 0.9, 1, 1.1, 1.25, 1.5, 2, 3},
		}, []string{"role"}),
		QueueWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "conloom_queue_wait_seconds", Help: "Time tasks wait for admission.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		}),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "conloom_queue_depth", Help: "Tasks currently waiting for admission.",
		}),
		AdmissionRejects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_admission_rejects_total", Help: "Admission rejections by reason.",
		}, []string{"reason"}),
		OutboxDelivered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "conloom_outbox_delivered_total", Help: "Outbox actions delivered.",
		}),
		OutboxRedelivered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "conloom_outbox_redelivered_total", Help: "Outbox actions redelivered after lease expiry.",
		}),
		OutboxExpired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "conloom_outbox_expired_total", Help: "Outbox actions expired without delivery.",
		}),
		Review: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_review_total", Help: "Review results by stage and verdict.",
		}, []string{"stage", "verdict"}),
		BreakerOpen: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "conloom_breaker_open", Help: "1 if the model circuit breaker for a role is open.",
		}, []string{"role"}),
		ContinuationExits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_continuation_exits_total", Help: "Continuation windows ended early by reason (rule|model).",
		}, []string{"reason"}),
		Proactive: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_proactive_total", Help: "Proactive replies by result (evaluated|sent|skipped).",
		}, []string{"result"}),
		MemoryConsolidations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_memory_consolidations_total", Help: "Memory consolidation runs by result (ok|error|skipped).",
		}, []string{"result"}),
		MemoryConsolidationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "conloom_memory_consolidation_seconds", Help: "Memory consolidation run duration.",
			Buckets: []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600},
		}),
		MemoryAffinityChanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_memory_affinity_changes_total", Help: "Affinity changes by source.",
		}, []string{"source"}),
		SkillCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_skill_calls_total", Help: "Tool calls made by the model, by tool and result (ok|error|unknown|invalid).",
		}, []string{"tool", "result"}),
		CleanupDeleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_cleanup_deleted_total",
			Help: "Rows deleted by retention cleanup, by table (tasks|outbox_actions|audit_events|events|proactive_log).",
		}, []string{"table"}),
		SearchSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "conloom_search_seconds", Help: "Recall search duration by path (keyword|vector).",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1, 2, 5},
		}, []string{"path"}),
		FTSBackfillRemaining: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "conloom_fts_backfill_remaining", Help: "Messages after the keyword index backfill mark.",
		}),
		EmbeddingRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_embedding_requests_total", Help: "Embedding service requests by result (ok|error|timeout).",
		}, []string{"result"}),
		EmbeddingBacklog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "conloom_embedding_backlog", Help: "Estimated chunks waiting to be embedded.",
		}),
		UsageDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "conloom_usage_dropped_total", Help: "Model call records dropped because the usage queue was full.",
		}),
		UsageWriteErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "conloom_usage_write_errors_total", Help: "Failed batch writes of model call records.",
		}),
		PanelLoginFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "conloom_panel_login_failures_total", Help: "Failed web panel logins.",
		}),
		PanelRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "conloom_panel_requests_total", Help: "Web panel requests by route and status code.",
		}, []string{"route", "code"}),
	}
	reg.MustRegister(
		m.Events, m.Tasks, m.ModelLatency, m.ModelTokens, m.EstimateRatio,
		m.QueueWait, m.QueueDepth, m.AdmissionRejects,
		m.OutboxDelivered, m.OutboxRedelivered, m.OutboxExpired,
		m.Review, m.BreakerOpen, m.ContinuationExits, m.Proactive,
		m.MemoryConsolidations, m.MemoryConsolidationSeconds, m.MemoryAffinityChanges,
		m.SkillCalls,
		m.SearchSeconds, m.FTSBackfillRemaining, m.EmbeddingRequests, m.EmbeddingBacklog,
		m.UsageDropped, m.UsageWriteErrors, m.PanelLoginFailures, m.PanelRequests,
	)
	reg.MustRegister(m.CleanupDeleted)
	return m
}
