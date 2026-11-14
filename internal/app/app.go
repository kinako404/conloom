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
	"github.com/kinako404/conloom/internal/panel"
	"github.com/kinako404/conloom/internal/panel/web"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/search/vector"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/usage"
)

// Options override defaults: a nil Registry is a fresh registry, a nil
// HTTPClient is http.DefaultClient (used for model calls), a nil Now is
// time.Now and nil Tools are the built-in tools. Version, Commit and
// StartedAt (the process start, New's time when zero) are shown in the panel.
type Options struct {
	Registry   *prometheus.Registry
	HTTPClient *http.Client
	Now        func() time.Time
	Tools      []skills.Tool
	// EmbeddingInterval is how often semantic search embeds new chunks,
	// every minute when 0.
	EmbeddingInterval time.Duration
	// PanelDeliveryInterval is how often the panel chat delivery looks for
	// due replies besides being woken when they become due, every second
	// when 0.
	PanelDeliveryInterval time.Duration

	Version, Commit string
	StartedAt       time.Time
}

// usageFlushTimeout bounds how long Close waits for queued model calls to be
// written.
const usageFlushTimeout = 5 * time.Second

// App is the assembled core service.
type App struct {
	st      *sqlite.DB
	orch    *core.Orchestrator
	sched   *memory.Scheduler
	usage   *usage.Recorder
	handler http.Handler
	// panelSite is the panel on its own listener, nil without panel.listen.
	panelSite http.Handler
	// panelOut delivers the actions of the panel channel.
	panelOut *outbox.PanelDeliverer
	// indexer and vectors are semantic search, both nil without an
	// embedding section in the config.
	indexer *vector.Indexer
	vectors search.VectorSearcher

	logger *slog.Logger
	// usageFlushTimeout is how long Close waits for queued model calls,
	// the usageFlushTimeout constant except in tests.
	usageFlushTimeout time.Duration

	closeOnce sync.Once
	closeErr  error
}

// New loads the skills, opens the database in the configured data directory
// (creating the directory if needed), registers the configured bots and wires
// every component, with the web panel when the config has a panel section. An
// invalid skill configuration is an error wrapping skills.ErrInvalid. The
// caller must Close the App.
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
	if opts.StartedAt.IsZero() {
		opts.StartedAt = now()
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
	panelOut := outbox.NewPanelDeliverer(ob, st, opts.PanelDeliveryInterval, now, logger)
	rec := usage.NewRecorder(st, usage.RecorderOptions{Metrics: metrics, Logger: logger})
	fail := func(err error) (*App, error) {
		_ = rec.Close(ctx)
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
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
		Usage:     rec,
	})
	memory.WarnOutputReservation(cfg, logger)
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
		indexer = vector.NewIndexer(vector.IndexerDeps{Interval: opts.EmbeddingInterval, Store: st,
			Embedder: usage.NewEmbedder(emb, rec, store.PurposeEmbedIndex), Policies: policies, Reviewer: reviewer,
			IsCommand: isCommand, Location: loc, Config: cfg, Metrics: metrics, Logger: logger})
		vectors = vector.NewSearcher(vector.SearcherDeps{Store: st, Embedder: usage.NewEmbedder(emb, rec, store.PurposeEmbedQuery),
			MinScore: e.MinScore, Margin: vector.DefaultMargin, Metrics: metrics, Logger: logger})
		logger.Info("semantic search enabled", "embedding_model", e.Model, "batch_size", e.BatchSize)
	}
	tools := opts.Tools
	if tools == nil {
		tools = builtinTools(toolDeps{Store: st, Policies: policies, Reviewer: reviewer, Metrics: metrics, Vectors: vectors})
	}
	skillReg, err := registerSkills(cfg, loadedSkills, tools)
	if err != nil {
		return fail(err)
	}
	orch, err := core.New(core.Deps{
		Store:         st,
		Router:        rt,
		Admission:     adm,
		Personas:      personas,
		Policies:      policies,
		Reviewer:      reviewer,
		Notifier:      core.Notifiers{ob, panelOut},
		Metrics:       metrics,
		Logger:        logger,
		Config:        cfg,
		Location:      loc,
		Now:           now,
		MemoryTrigger: sched,
		Skills:        skillReg,
		Usage:         rec,
	})
	if err != nil {
		return fail(err)
	}
	memAdmin := memory.NewAdmin(memory.AdminDeps{Store: st, Policies: policies, Metrics: metrics, Now: now})
	presence := obs.NewPresence()
	var panelHandler, panelSite http.Handler
	if cfg.Panel != nil {
		panelHandler = panel.New(panel.Deps{
			Config:      cfg,
			Sessions:    st,
			Audit:       st,
			Assets:      web.FS,
			Metrics:     metrics,
			Logger:      logger,
			Now:         now,
			Store:       st,
			Policies:    policies,
			Personas:    personas,
			MemoryAdmin: memAdmin,
			Memory:      sched,
			Gatherer:    opts.Registry,
			Presence:    presence,
			Location:    loc,
			Version:     opts.Version,
			Commit:      opts.Commit,
			StartedAt:   opts.StartedAt,
			Chat:        orch,
		}).Handler()
		if cfg.Panel.Listen != "" {
			panelSite, panelHandler = panelHandler, nil
		}
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
		MemoryAdmin:  memAdmin,
		Presence:     presence,
		Panel:        panelHandler,
	})
	return &App{st: st, orch: orch, panelOut: panelOut, sched: sched, usage: rec, handler: gw.Handler(), panelSite: panelSite,
		indexer: indexer, vectors: vectors, logger: logger, usageFlushTimeout: usageFlushTimeout}, nil
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

// Handler serves the Bot Interaction API, /healthz and, if enabled, /metrics
// and the web panel, unless the panel has its own listener.
func (a *App) Handler() http.Handler { return a.handler }

// PanelHandler serves the web panel on the address panel.listen; it is nil
// when panel.listen is not set and the panel, if any, is part of Handler.
func (a *App) PanelHandler() http.Handler { return a.panelSite }

// VectorSearcher is the semantic search over the conversations' chunks, or
// nil when the config has no embedding section.
func (a *App) VectorSearcher() search.VectorSearcher { return a.vectors }

// Start recovers tasks left unfinished by a previous process and starts
// delivering panel chat replies and background maintenance, including memory
// consolidation and, with an embedding section in the config, embedding new
// chunks. It must be called once, before the API serves traffic; later calls
// return core.ErrAlreadyRecovered.
func (a *App) Start(ctx context.Context) error {
	if err := a.orch.Recover(ctx); err != nil {
		return err
	}
	a.panelOut.Start()
	a.sched.Start()
	if a.indexer != nil {
		a.indexer.Start()
	}
	return nil
}

// Close stops memory consolidation, embedding, the orchestrator and then the
// panel chat delivery, writes the queued model calls and closes the database,
// returning the database's error. Model calls not written within 5s are lost
// with a warning, like any other failed usage write. Further calls return the
// first result.
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.sched.Close()
		if a.indexer != nil {
			a.indexer.Close()
		}
		a.orch.Close()
		a.panelOut.Close()
		ctx, cancel := context.WithTimeout(context.Background(), a.usageFlushTimeout)
		defer cancel()
		if err := a.usage.Close(ctx); err != nil {
			a.logger.Warn("queued model calls not written before shutdown; the rest are lost",
				"timeout", a.usageFlushTimeout, "error", err)
		}
		a.closeErr = a.st.Close()
	})
	return a.closeErr
}
