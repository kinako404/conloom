// Package app assembles the ConLoom core service from its configuration.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/embedding"
	"github.com/kinako404/conloom/internal/gateway"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/outbox"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/search/vector"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store/sqlite"
)

// Options override defaults: a nil Registry is a fresh registry, a nil
// HTTPClient is http.DefaultClient (used for model calls), a nil Now is
// time.Now and nil Tools are the built-in tools.
type Options struct {
	Registry   *prometheus.Registry
	HTTPClient *http.Client
	Now        func() time.Time
	Tools      []skills.Tool
}

// App is the assembled core service.
type App struct {
	st      *sqlite.DB
	orch    *core.Orchestrator
	sched   *memory.Scheduler
	handler http.Handler
	// indexer and vectors are semantic search, both nil without an
	// embedding section in the config.
	indexer *vector.Indexer
	vectors search.VectorSearcher

	closeOnce sync.Once
	closeErr  error
}

// New loads the skills, opens the database in the configured data directory
// (creating the directory if needed), registers the configured bots and wires
// every component. An invalid skill configuration is an error wrapping
// skills.ErrInvalid. The caller must Close the App.
func New(cfg *config.Config, logger *slog.Logger, opts Options) (*App, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if opts.Registry == nil {
		opts.Registry = prometheus.NewRegistry()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	loadedSkills, err := skills.Load(cfg.Skills.Dir)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}

	if err := os.MkdirAll(cfg.Storage.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("app: create data directory: %w", err)
	}
	ctx := context.Background()
	st, err := sqlite.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, fmt.Errorf("app: open database %s: %w", cfg.DBPath(), err)
	}
	for _, b := range cfg.Bots {
		if ids := b.UnusedPrivateConversations(); len(ids) > 0 {
			logger.Warn("private enabled_conversations take effect only when private_chat is allowlist; ignoring them",
				"bot", b.ID, "private_chat", b.PrivateChat, "platform_ids", ids)
		}
		if err := st.UpsertBotInstance(ctx, b.ID, b.Platform, b.Name); err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("app: register bot %s: %w", b.ID, err)
		}
	}

	var profiles []*router.Profile
	limits := map[string]admission.Limits{}
	for _, name := range slices.Sorted(maps.Keys(cfg.ModelProfiles)) {
		p := cfg.ModelProfiles[name]
		profiles = append(profiles, &router.Profile{Role: router.Role(name), Config: p, Client: openai.New(p, opts.HTTPClient)})
		limits[name] = admission.Limits{MaxConcurrency: p.Runtime.MaxConcurrency, TokenBudget: p.Runtime.GPUKVCacheTokens}
	}

	metrics := obs.NewMetrics(opts.Registry)
	loc := location(cfg.Server.Timezone)
	rt := router.New(profiles, router.BreakerConfig{}, now)
	adm := admission.New(limits)
	personas := persona.NewService(st, cfg.PersonaTemplates)
	policies := policy.NewService(st, cfg.DefaultGroupPolicy).WithPrivateDefaults(cfg.PrivatePolicy())
	ob := outbox.New(st, outbox.Config{Lease: cfg.Runtime.OutboxLease.Duration}, metrics, now)
	cons := memory.NewConsolidator(memory.ConsolidatorDeps{
		Store:     st,
		Router:    rt,
		Admission: adm,
		Personas:  personas,
		Policies:  policies,
		Config:    cfg,
		Metrics:   metrics,
		Logger:    logger,
		Location:  loc,
		Now:       now,
	})
	sched := memory.NewScheduler(memory.SchedulerDeps{
		Store:    st,
		Policies: policies,
		Runner:   cons,
		Config:   cfg,
		Metrics:  metrics,
		Logger:   logger,
		Location: loc,
		Now:      now,
	})
	policies.OnChange(sched.PolicyChanged)
	reviewer := review.New(cfg.Secrets(), review.Options{})
	var indexer *vector.Indexer
	var vectors search.VectorSearcher
	if e := cfg.Embedding; e != nil {
		emb := embedding.New(*e, nil)
		indexer = vector.NewIndexer(vector.IndexerDeps{Store: st, Embedder: emb, Policies: policies, Reviewer: reviewer,
			IsCommand: isCommand, Location: loc, Config: cfg, Metrics: metrics, Logger: logger})
		vectors = vector.NewSearcher(vector.SearcherDeps{Store: st, Embedder: emb, Metrics: metrics, Logger: logger})
		logger.Info("semantic search enabled", "embedding_model", e.Model, "batch_size", e.BatchSize)
	}
	tools := opts.Tools
	if tools == nil {
		tools = builtinTools(toolDeps{Store: st, Policies: policies, Reviewer: reviewer, Metrics: metrics, Vectors: vectors})
	}
	skillReg, err := registerSkills(cfg, loadedSkills, tools)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
	orch, err := core.New(core.Deps{
		Store:         st,
		Router:        rt,
		Admission:     adm,
		Personas:      personas,
		Policies:      policies,
		Reviewer:      reviewer,
		Notifier:      ob,
		Metrics:       metrics,
		Logger:        logger,
		Config:        cfg,
		Location:      loc,
		Now:           now,
		MemoryTrigger: sched,
		Skills:        skillReg,
	})
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
	gw := gateway.New(gateway.Deps{
		Orchestrator: orch,
		Outbox:       ob,
		Personas:     personas,
		Policies:     policies,
		Store:        st,
		Config:       cfg,
		Metrics:      promhttp.HandlerFor(opts.Registry, promhttp.HandlerOpts{}),
		Logger:       logger,
		Memory:       sched,
		Counters:     metrics,
	})
	return &App{st: st, orch: orch, sched: sched, handler: gw.Handler(), indexer: indexer, vectors: vectors}, nil
}

// location is the time zone tz, falling back to Asia/Shanghai, then UTC.
func location(tz string) *time.Location {
	for _, name := range []string{tz, "Asia/Shanghai"} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.UTC
}

// Handler serves the Bot Interaction API, /healthz and (if enabled) /metrics.
func (a *App) Handler() http.Handler { return a.handler }

// VectorSearcher is the semantic search over the conversations' chunks, or
// nil when the config has no embedding section.
func (a *App) VectorSearcher() search.VectorSearcher { return a.vectors }

// Start recovers tasks left unfinished by a previous process and starts
// background maintenance, including memory consolidation and, with an
// embedding section in the config, embedding new chunks. It must be called
// once, before the API serves traffic; later calls return
// core.ErrAlreadyRecovered.
func (a *App) Start(ctx context.Context) error {
	if err := a.orch.Recover(ctx); err != nil {
		return err
	}
	a.sched.Start()
	if a.indexer != nil {
		a.indexer.Start()
	}
	return nil
}

// Close stops memory consolidation, embedding and the orchestrator, then
// closes the database. Further calls return the first result.
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.sched.Close()
		if a.indexer != nil {
			a.indexer.Close()
		}
		a.orch.Close()
		a.closeErr = a.st.Close()
	})
	return a.closeErr
}
