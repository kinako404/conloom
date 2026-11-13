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
	"github.com/kinako404/conloom/internal/gateway"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/outbox"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store/sqlite"
)

// Options override defaults: a nil Registry is a fresh registry, a nil
// HTTPClient is http.DefaultClient (used for model calls) and a nil Now is
// time.Now.
type Options struct {
	Registry   *prometheus.Registry
	HTTPClient *http.Client
	Now        func() time.Time
}

// App is the assembled core service.
type App struct {
	st      *sqlite.DB
	orch    *core.Orchestrator
	sched   *memory.Scheduler
	handler http.Handler

	closeOnce sync.Once
	closeErr  error
}

// New opens the database in the configured data directory (creating the
// directory if needed), registers the configured bots and wires every
// component. The caller must Close the App.
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

	if err := os.MkdirAll(cfg.Storage.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("app: create data directory: %w", err)
	}
	ctx := context.Background()
	st, err := sqlite.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, fmt.Errorf("app: open database %s: %w", cfg.DBPath(), err)
	}
	for _, b := range cfg.Bots {
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
		Metrics:  metrics,
		Logger:   logger,
		Location: loc,
		Now:      now,
	})
	policies.OnChange(sched.PolicyChanged)
	orch, err := core.New(core.Deps{
		Store:         st,
		Router:        rt,
		Admission:     adm,
		Personas:      personas,
		Policies:      policies,
		Reviewer:      review.New(cfg.Secrets(), review.Options{}),
		Notifier:      ob,
		Metrics:       metrics,
		Logger:        logger,
		Config:        cfg,
		Location:      loc,
		Now:           now,
		MemoryTrigger: sched,
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
	return &App{st: st, orch: orch, sched: sched, handler: gw.Handler()}, nil
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

// Start recovers tasks left unfinished by a previous process and starts
// background maintenance, including memory consolidation. It must be called
// once, before the API serves traffic; later calls return
// core.ErrAlreadyRecovered.
func (a *App) Start(ctx context.Context) error {
	if err := a.orch.Recover(ctx); err != nil {
		return err
	}
	a.sched.Start()
	return nil
}

// Close stops memory consolidation and the orchestrator, then closes the
// database. Further calls return the first result.
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.sched.Close()
		a.orch.Close()
		a.closeErr = a.st.Close()
	})
	return a.closeErr
}
