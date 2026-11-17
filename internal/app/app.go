// Package app assembles the ConLoom core service from its configuration.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/botdir"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/embedding"
	"github.com/kinako404/conloom/internal/gateway"
	"github.com/kinako404/conloom/internal/ids"
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
	"github.com/kinako404/conloom/pkg/conloomapi"
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
	st *sqlite.DB
	// policies notifies conversations of default policy changes in the
	// background until Close.
	policies *policy.Service
	orch     *core.Orchestrator
	sched    *memory.Scheduler
	usage    *usage.Recorder
	handler  http.Handler
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
// (creating the directory if needed), migrates the persona templates of an
// old config file into the database once (see migrateLegacyTemplates),
// merges the memory older versions kept per panel chat into the bots' shared
// scopes once (migratePanelMemory), loads the persona templates, stores the
// first default policies, importing those of an old config file
// (migratePolicyDefaults), and loads the stored ones, migrates the bot
// settings of an old config file into the database once (migrateBotSettings),
// wires every component to one bot directory, which holds the config bots
// with their stored settings and the panel bots stored in the database,
// registers the config bots and adds the web panel when the config has a
// panel section. An invalid skill configuration is an error wrapping
// skills.ErrInvalid, and persona_templates, default policies or bot settings
// still in the config file after they were migrated one wrapping
// store.ErrAlreadyMigrated. The caller must Close the App.
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

	var profiles []*router.Profile
	limits := map[string]admission.Limits{}
	for _, name := range slices.Sorted(maps.Keys(cfg.ModelProfiles)) {
		p := cfg.ModelProfiles[name]
		profiles = append(profiles, &router.Profile{Role: router.Role(name), Config: p, Client: openai.New(p, opts.HTTPClient)})
		limits[name] = admission.Limits{MaxConcurrency: p.Runtime.MaxConcurrency, TokenBudget: p.Runtime.GPUKVCacheTokens}
	}

	// Until PanelBots().Load below publishes their stored settings, the config
	// bots have the defaults.
	bots := botdir.New(botdir.WithDefaults(cfg.Bots))
	metrics := obs.NewMetrics(opts.Registry)
	loc := location(cfg.Server.Timezone)
	rt := router.New(profiles, router.BreakerConfig{}, now)
	metrics.WatchBreakers(slices.Sorted(maps.Keys(cfg.ModelProfiles)), func(role string) bool {
		return rt.BreakerOpen(router.Role(role))
	})
	adm := admission.New(limits)
	personas := persona.NewService(st)
	personas.SetLogger(logger)
	personas.SetTemplateUsers(bots.TemplateUsers)
	if err := migrateLegacyTemplates(ctx, st, cfg.LegacyPersonaTemplates, now().UTC(), loc, logger); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
	if err := migratePanelMemory(ctx, st, now().UTC(), logger); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
	if err := personas.LoadTemplates(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
	if err := migratePolicyDefaults(ctx, st, cfg, now().UTC(), loc, logger); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
	policies := policy.NewService(st, cfg.DefaultGroupPolicy).WithPrivateDefaults(cfg.PrivatePolicy()).
		WithReservedContextTokens(cfg.ReservedContextTokens()).WithLogger(logger)
	if err := policies.LoadDefaults(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("app: %w", err)
	}
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
		Bots:      bots,
		Metrics:   metrics,
		Logger:    logger,
		Location:  loc,
		Now:       now,
		Usage:     rec,
	})
	groupDefaults, privateDefaults := policies.Defaults(policy.KindGroup).Policy, policies.Defaults(policy.KindPrivate).Policy
	memory.WarnOutputReservation(cfg, groupDefaults, privateDefaults, logger)
	memory.WarnPrivateMemoryNotSensitive(cfg, privateDefaults, logger)
	sched := memory.NewScheduler(memory.SchedulerDeps{
		Store:    st,
		Policies: policies,
		Runner:   cons,
		Bots:     bots,
		Metrics:  metrics,
		Logger:   logger,
		Location: loc,
		Now:      now,
	})
	policies.OnTransition(sched.PolicyChanged)
	reviewer := review.New(cfg.Secrets(), review.Options{})
	var indexer *vector.Indexer
	var vectors search.VectorSearcher
	if e := cfg.Embedding; e != nil {
		emb := embedding.New(*e, nil)
		indexer = vector.NewIndexer(vector.IndexerDeps{Interval: opts.EmbeddingInterval, Store: st,
			Embedder: usage.NewEmbedder(emb, rec, store.PurposeEmbedIndex), Policies: policies, Reviewer: reviewer,
			IsCommand: isCommand, Location: loc, Bots: bots, Metrics: metrics, Logger: logger})
		vectors = vector.NewSearcher(vector.SearcherDeps{Store: st, Embedder: usage.NewEmbedder(emb, rec, store.PurposeEmbedQuery),
			MinScore: e.MinScore, Margin: vector.DefaultMargin, Metrics: metrics, Logger: logger})
		logger.Info("semantic search enabled", "embedding_model", e.Model, "batch_size", e.BatchSize)
	}
	tools := opts.Tools
	if tools == nil {
		tools = builtinTools(toolDeps{Store: st, Policies: policies, Personas: personaSource{bots: bots, personas: personas},
			Reviewer: reviewer, Metrics: metrics, Vectors: vectors})
	}
	skillReg, err := registerSkills(cfg, loadedSkills, tools)
	if err != nil {
		return fail(err)
	}
	if err := migrateBotSettings(ctx, st, cfg.Bots, now().UTC(), loc, logger); err != nil {
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
		Bots:          bots,
		Location:      loc,
		Now:           now,
		MemoryTrigger: sched,
		Skills:        skillReg,
		Usage:         rec,
	})
	if err != nil {
		return fail(err)
	}
	if err := orch.PanelBots().Load(ctx); err != nil {
		orch.Close()
		return fail(err)
	}
	if err := registerConfigBots(ctx, st, bots, logger); err != nil {
		orch.Close()
		return fail(err)
	}
	memAdmin := memory.NewAdmin(memory.AdminDeps{Store: st, Policies: policies, Metrics: metrics, Now: now})
	presence := obs.NewPresence()
	var panelHandler, panelSite http.Handler
	if cfg.Panel != nil {
		panelHandler = panel.New(panel.Deps{
			Config:      cfg,
			Bots:        bots,
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
			Autonomy:    orch.Autonomy(),
			PanelBots:   orch.PanelBots(),
			Appearance:  orch,
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
		Bots:         bots,
		Metrics:      promhttp.HandlerFor(opts.Registry, promhttp.HandlerOpts{}),
		Logger:       logger,
		Memory:       sched,
		Counters:     metrics,
		MemoryAdmin:  memAdmin,
		Presence:     presence,
		Panel:        panelHandler,
	})
	return &App{st: st, policies: policies, orch: orch, panelOut: panelOut, sched: sched, usage: rec, handler: gw.Handler(),
		panelSite: panelSite, indexer: indexer, vectors: vectors, logger: logger, usageFlushTimeout: usageFlushTimeout}, nil
}

// migrateLegacyTemplates migrates the persona templates legacy of an old
// config file into the database on the first start with them (see
// sqlite.DB.MigrateConfigTemplates): a name the database has already keeps
// the stored template. It logs each template, audits the migration as
// persona.template.migrate and warns that persona_templates must be removed
// from the config file. Without legacy it does nothing. Once the templates
// were migrated, legacy is an error in Chinese wrapping
// store.ErrAlreadyMigrated, which the deployer fixes in the config file.
func migrateLegacyTemplates(ctx context.Context, st *sqlite.DB, legacy map[string]conloomapi.PersonaDoc, now time.Time,
	loc *time.Location, logger *slog.Logger) error {
	if len(legacy) == 0 {
		return nil
	}
	rows := make([]store.PersonaTemplateRecord, 0, len(legacy))
	for _, name := range slices.Sorted(maps.Keys(legacy)) {
		rows = append(rows, store.PersonaTemplateRecord{Name: name, Persona: legacy[name], UpdatedBy: "system"})
	}
	imported, skipped, err := st.MigrateConfigTemplates(ctx, rows, now)
	if errors.Is(err, store.ErrAlreadyMigrated) {
		return fmt.Errorf("配置文件里的 persona_templates %s导入数据库，人设模板现在只在 Web 管理面板里管理；"+
			"请删除配置文件里整段 persona_templates 后再启动（%w）", migratedWhen(ctx, st, store.MetaConfigTemplatesMigrated, loc), err)
	}
	if err != nil {
		return fmt.Errorf("migrate persona templates: %w", err)
	}
	for _, name := range imported {
		logger.Info("persona template imported from the config file", "template", name)
	}
	for _, name := range skipped {
		logger.Info("persona template already in the database; keeping it, not the config file's", "template", name)
	}
	detail, err := json.Marshal(struct {
		Imported []string `json:"imported"`
		Skipped  []string `json:"skipped"`
	}{append([]string{}, imported...), append([]string{}, skipped...)})
	if err == nil {
		err = st.InsertAudit(ctx, store.AuditEvent{ID: ids.New("aud"), Kind: "persona.template.migrate", Actor: "system",
			Detail: string(detail), CreatedAt: now})
	}
	if err != nil {
		logger.Error("persona templates migrated but audit failed", "error", err)
	}
	logger.Warn("the config file's persona templates are now in the database and managed in the web panel; "+
		"remove persona_templates from the config file, or the next start fails",
		"imported", len(imported), "skipped", len(skipped))
	return nil
}

// migratedWhen says in Chinese when the migration recorded under the
// app_meta key was done, for a sentence "… <when>导入数据库": "已在 <local
// time> ", or "已经" when the time cannot be read.
func migratedWhen(ctx context.Context, st *sqlite.DB, key string, loc *time.Location) string {
	if v, ok, _ := st.GetMeta(ctx, key); ok {
		if at, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return "已在 " + at.In(loc).Format("2006-01-02 15:04:05") + " "
		}
	}
	return "已经"
}

// migrateBotSettings migrates the settings keys an old config file still
// has in bots[] (config.BotConfig.Legacy) into the database on the first
// start with any of them (see sqlite.DB.MigrateConfigBotSettings): every
// config bot without stored settings gets the ones the old version ran it
// with (config.BotConfig.WithLegacySettings), and a bot with stored settings
// keeps them. It logs each bot, audits the migration as bot.settings.migrate
// and warns that the keys must be removed from the config file. Without such
// keys it does nothing, and config bots without stored settings take the
// defaults. Once the settings were migrated, such keys are an error in
// Chinese naming them and wrapping store.ErrAlreadyMigrated, which the
// deployer fixes in the config file.
func migrateBotSettings(ctx context.Context, st *sqlite.DB, bots []config.BotConfig, now time.Time,
	loc *time.Location, logger *slog.Logger) error {
	var withKeys []string
	for i, b := range bots {
		if keys := b.Legacy.Keys(); len(keys) > 0 {
			withKeys = append(withKeys, fmt.Sprintf("bots[%d]（%s）的 %s", i, b.ID, strings.Join(keys, "、")))
		}
	}
	if len(withKeys) == 0 {
		return nil
	}
	migrated, err := st.MigrateConfigBotSettings(ctx, botdir.LegacySettings(bots), now)
	if errors.Is(err, store.ErrAlreadyMigrated) {
		return fmt.Errorf("配置文件里 bot 的设置%s导入数据库，显示名、管理员、私聊开放范围、启用的群、默认人设模板和技能现在只在 "+
			"Web 管理面板里改；请删除配置文件里 %s，bots[] 每项只留 id、platform、platform_self_id，然后再启动（%w）",
			migratedWhen(ctx, st, store.MetaConfigBotSettingsMigrated, loc), strings.Join(withKeys, "，"), err)
	}
	if err != nil {
		return fmt.Errorf("migrate bot settings: %w", err)
	}
	skipped := []string{}
	for _, b := range bots {
		if slices.Contains(migrated, b.ID) {
			logger.Info("bot settings imported from the config file", "bot", b.ID)
		} else {
			skipped = append(skipped, b.ID)
			logger.Info("bot settings already in the database; keeping them, not the config file's", "bot", b.ID)
		}
	}
	detail, err := json.Marshal(struct {
		Migrated []string `json:"migrated"`
		Skipped  []string `json:"skipped"`
	}{append([]string{}, migrated...), skipped})
	if err == nil {
		err = st.InsertAudit(ctx, store.AuditEvent{ID: ids.New("aud"), Kind: "bot.settings.migrate", Actor: "system",
			Detail: string(detail), CreatedAt: now})
	}
	if err != nil {
		logger.Error("bot settings migrated but audit failed", "error", err)
	}
	logger.Warn("the config file's bot settings are now in the database and edited in the web panel; remove name, admins, "+
		"private_chat, enabled_conversations, persona_template and skills from bots[] in the config file, or the next start fails",
		"migrated", len(migrated), "skipped", len(skipped))
	return nil
}

// migratePolicyDefaults stores the first group and private default policies
// (see sqlite.DB.InitPolicyDefaults): a section the config file has
// (config.Config.PolicySections) is imported as version 1 of its kind by
// system, and a kind without a stored default policy or a section gets the
// config's, which are the built-in ones unless a config built in Go changed
// them. When it imported a section it logs each, audits the import as
// settings.policy.migrate and warns that the sections must be removed from the
// config file. Once a kind has a stored default policy, its section in the
// file is an error in Chinese naming the sections and wrapping
// store.ErrAlreadyMigrated, which the deployer fixes in the config file; the
// default policies are then edited only in the panel's settings page.
func migratePolicyDefaults(ctx context.Context, st *sqlite.DB, cfg *config.Config, now time.Time,
	loc *time.Location, logger *slog.Logger) error {
	group, private := cfg.PolicySections()
	var imports []store.PolicyDefaults
	var sections []string
	if group {
		imports = append(imports, store.PolicyDefaults{Kind: policy.KindGroup, Policy: cfg.DefaultGroupPolicy,
			Reason: "从配置文件导入", UpdatedBy: "system"})
		sections = append(sections, "default_group_policy")
	}
	if private {
		imports = append(imports, store.PolicyDefaults{Kind: policy.KindPrivate, Policy: cfg.PrivatePolicy(),
			Reason: "从配置文件导入", UpdatedBy: "system"})
		sections = append(sections, "default_private_policy")
	}
	builtins := []store.PolicyDefaults{
		{Kind: policy.KindGroup, Policy: cfg.DefaultGroupPolicy, Reason: "内置默认值", UpdatedBy: "system"},
		{Kind: policy.KindPrivate, Policy: cfg.PrivatePolicy(), Reason: "内置默认值", UpdatedBy: "system"},
	}
	imported, err := st.InitPolicyDefaults(ctx, imports, builtins, now)
	if errors.Is(err, store.ErrAlreadyMigrated) {
		when := "第一次启动时写入的内置默认值"
		if _, ok, _ := st.GetMeta(ctx, store.MetaConfigPolicyDefaultsMigrated); ok {
			when = migratedWhen(ctx, st, store.MetaConfigPolicyDefaultsMigrated, loc) + "从配置文件导入"
		}
		return fmt.Errorf("数据库里已经有默认策略（%s），群聊、私聊默认策略现在只在 Web 管理面板的设置页里改；请删除配置文件里的 %s，然后再启动（%w）",
			when, strings.Join(sections, "、"), err)
	}
	if err != nil {
		return fmt.Errorf("store default policies: %w", err)
	}
	if len(imported) == 0 {
		return nil
	}
	for _, kind := range imported {
		logger.Info("default policy imported from the config file", "kind", kind)
	}
	detail, err := json.Marshal(struct {
		Imported []string `json:"imported"`
	}{imported})
	if err == nil {
		err = st.InsertAudit(ctx, store.AuditEvent{ID: ids.New("aud"), Kind: "settings.policy.migrate", Actor: "system",
			Detail: string(detail), CreatedAt: now})
	}
	if err != nil {
		logger.Error("default policies imported but audit failed", "error", err)
	}
	logger.Warn("the config file's default policies are now in the database and edited in the web panel's settings page; " +
		"remove default_group_policy and default_private_policy from the config file, or the next start fails")
	return nil
}

// registerConfigBots registers the config bots of bots, as they take effect,
// as bot instances, warning about private enabled conversations that do not
// take effect.
func registerConfigBots(ctx context.Context, st *sqlite.DB, bots *botdir.Directory, logger *slog.Logger) error {
	for _, configured := range bots.ConfigBots() {
		b, _ := bots.Bot(configured.ID)
		if ids := b.UnusedPrivateConversations(); len(ids) > 0 {
			logger.Warn("the private allowlist takes effect only when private_chat is allowlist; ignoring it",
				"bot", b.ID, "private_chat", b.PrivateChat, "platform_ids", ids)
		}
		if err := st.UpsertBotInstance(ctx, b.ID, b.Platform, b.Name); err != nil {
			return fmt.Errorf("register bot %s: %w", b.ID, err)
		}
	}
	return nil
}

// migratePanelMemory merges the memory that older versions kept per panel
// chat into the bots' shared panel memory scopes on the first start that has
// shared scopes (see sqlite.DB.MigratePanelMemoryScope). It logs how many
// chats each bot had merged and, when any were, audits the migration as
// memory.scope.migrate; a failed audit is only logged.
func migratePanelMemory(ctx context.Context, st *sqlite.DB, now time.Time, logger *slog.Logger) error {
	merged, ran, err := st.MigratePanelMemoryScope(ctx, now)
	if err != nil {
		return fmt.Errorf("merge panel chat memory into shared scopes: %w", err)
	}
	if !ran {
		return nil
	}
	type bot struct {
		Bot   string `json:"bot"`
		Chats int    `json:"chats"`
	}
	bots := make([]bot, 0, len(merged))
	for _, m := range merged {
		logger.Info("panel chat memory merged into the bot's shared scope", "bot", m.BotInstanceID, "chats", len(m.Chats))
		bots = append(bots, bot{m.BotInstanceID, len(m.Chats)})
	}
	if len(merged) == 0 {
		return nil
	}
	detail, err := json.Marshal(struct {
		Bots []bot `json:"bots"`
	}{bots})
	if err == nil {
		err = st.InsertAudit(ctx, store.AuditEvent{ID: ids.New("aud"), Kind: "memory.scope.migrate", Actor: "system",
			Detail: string(detail), CreatedAt: now})
	}
	if err != nil {
		logger.Error("panel chat memory merged but audit failed", "error", err)
	}
	return nil
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

// Close stops notifying conversations of default policy changes, then memory
// consolidation, embedding, the orchestrator and the panel chat delivery,
// writes the queued model calls and closes the database, returning the
// database's error. Model calls not written within 5s are lost with a
// warning, like any other failed usage write. Further calls return the first
// result.
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.policies.Close()
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
