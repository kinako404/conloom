# 面板"设置"页：默认策略搬进数据库 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 群聊、私聊两段默认策略存进数据库（有版本、审计、可回滚），面板新增"设置"页修改并立即生效；第一次启动从配置文件导入，之后文件里再有这两段就退出码 78；会话"策略"页可以"改回跟随默认"。

**Architecture:** 迁移 `0021` 加 `policy_defaults` 表和 `group_policies.follows_defaults` 列。`policy.Service` 启动时从库里读默认值（`LoadDefaults`），`SetDefaults` 在锁内追加版本、替换默认值、清空整个缓存，再在后台按批通知跟随默认的会话：调用和"会话自己的策略改了"相同的回调（`OnChange`），外加新回调 `OnTransition(convKey, before, after)` 带改前、改后的生效策略，记忆调度器改用它判断"记忆刚打开"。`app.New` 按设计第 4 节四种情况导入或写入内置值（照 bot 设置迁移）。面板加三个设置接口和一个"改回跟随默认"接口；前端把会话页的常用项表单抽成 `views/policyform.js`，新增 `views/settings.js`。

**Tech Stack:** Go（见 go.mod），SQLite，原生 JS + CSS（`internal/panel/web`，无构建、无依赖）。

**Spec:** `docs/superpowers/specs/2026-11-18-settings-tab-design.md`

## Global Constraints

- 公开仓库：代码、测试、文档、提交说明里只用假数据（`qq-main`、`bot_main`、用户 20001/29999、群 10001、小明/小红、`example.invalid`）。
- 不加新依赖（Go 和前端都不加）；不引入前端构建步骤。
- 每个任务提交前：`gofmt -l .` 为空、`go vet ./...` 通过、相关包 `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1` 通过；改了 JS 的任务对每个改动的 JS 文件跑 `/usr/local/node/bin/node --check <file>`。最后一个任务跑全量 `./...`。
- 提交说明英文 conventional 风格；界面文案中文，逐字使用设计和本计划给出的文案。
- 逐字沿用设计的名字和值：
  - 表 `policy_defaults`，列 `kind`、`version`、`policy`（JSON）、`reason`、`updated_by`、`created_at`，主键 `(kind, version)`；`group_policies` 新列 `follows_defaults INTEGER NOT NULL DEFAULT 0`；迁移编号 `0021`。
  - kind 只有 `group`、`private`（等于 `conloomapi.ConvGroup`、`conloomapi.ConvPrivate`）。
  - app_meta 键 `config_policy_defaults_migrated`；导入行 `updated_by=system`、`reason` 为 `从配置文件导入`；已导入（或库里已有默认值）而文件仍有这两段 → 包 `store.ErrAlreadyMigrated`，`serve` 退出码 78。
  - 路由：`GET /panel/api/settings/policies`、`PUT /panel/api/settings/policies/{kind}`、`POST /panel/api/settings/policies/{kind}/restore`、`POST /panel/api/conversations/{key}/policy/follow-defaults`；都走 `s.api`（会话鉴权 + CSRF）。`/api/v1` 不加任何接口。
  - 审计：`settings.policy.update` detail `{kind, version, action}`，action `update`|`restore`；`policy.update` detail `{action: "follow_defaults", version}`；`settings.policy.migrate`。
  - 导航顺序：概览、用量、聊天、会话、人设模板、任务、审计、设置、配置；路由 `#/settings`；TG 页 ≡ 菜单共用 `lib/sections.js`，不另抄。
  - 设置页每块顶部：`当前有 N 个会话跟随这个默认值；单独改过策略的会话不受影响。`
- 反目标（设计 2 节）每条都有测试：跟随默认的会话拿到新值且收到通知；保存非法默认值被拒、服务照常；单独设置过的会话不受默认值改动影响。

## Review Focus

1. 改默认值后跟随默认的会话没收到通知（暂停、主动、记忆开关没跟着变）——T2 `TestPolicy_SetDefaultsNotifiesFollowersOnly`、T3 `TestOrchestrator_DefaultsChangePausesFollowers`、`TestScheduler_TransitionTurningMemoryOnJumps`。
2. `SetDefaults` 后缓存没清，跟随者还读到旧合并值——T2 `TestPolicy_SetDefaultsClearsCache`。
3. 首次启动的三种文件形态：两段都没有、只有一段、两段都有——T3 `TestPolicyDefaults_FreshInstallSeedsBuiltins`、`TestPolicyDefaults_OnlyGroupSection`、`TestPolicyDefaults_ImportOnFirstStart`。
4. 导入后文件里仍留着这两段再启动 → 退出码 78、库不变——T3 `TestPolicyDefaults_RestartWithKeysFails`（另有 cmd 层断言）。
5. 非法默认值（并发 3、上下文上限 ≤ 预留、未知 kind）被拒；暂停中的会话"改回跟随默认"后按默认值取消暂停——T2 `TestPolicy_SetDefaultsRejectsInvalid`、`TestPolicy_FollowDefaultsUnpauses`，T4 `TestSettingsPolicies_PutInvalid`、`TestFollowDefaults_Endpoint`。

---

### Task 1: 存储——迁移 0021、默认策略表、跟随标记

**Files:**
- Create: `internal/store/sqlite/migrations/0021_policy_defaults.sql`
- Create: `internal/store/sqlite/policydefaults.go`（新方法）
- Modify: `internal/store/records.go`（`PolicyVersion` 加字段、新 `PolicyDefaults`）
- Modify: `internal/store/store.go`（接口 :207-210 附近加方法；新常量）
- Modify: `internal/store/sqlite/versions.go`（`policyVersionCols`、`scanPolicyVersion`、`AppendPolicyVersion` 带上新列）
- Test: `internal/store/sqlite/policydefaults_test.go`（新）、`internal/store/sqlite/versions_test.go`

**Interfaces:**
- SQL：`CREATE TABLE policy_defaults (kind TEXT NOT NULL, version INTEGER NOT NULL, policy TEXT NOT NULL, reason TEXT NOT NULL, updated_by TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (kind, version));` `ALTER TABLE group_policies ADD COLUMN follows_defaults INTEGER NOT NULL DEFAULT 0;` 文件头注释说明两者含义（照 0020 的风格）。
- `store.PolicyVersion` 加 `FollowsDefaults bool`（库里的列；`policy.Service.Current` 对没存过策略的会话也设为 true，见 T2）。
- `store.PolicyDefaults{Kind string; Version int; Policy conloomapi.GroupPolicy; Reason, UpdatedBy string; CreatedAt time.Time}`
- `const store.MetaConfigPolicyDefaultsMigrated = "config_policy_defaults_migrated"`（注释照 `MetaConfigBotSettingsMigrated`）；`ErrAlreadyMigrated` 的注释补上"默认策略"。
- 加进 `store.Store` 接口（只有 sqlite 实现它，已核实）：
  - `LatestPolicyDefaults(ctx context.Context, kind string) (PolicyDefaults, error)`——没有时 `ErrNotFound`
  - `ListPolicyDefaults(ctx context.Context, kind string, limit int) ([]PolicyDefaults, error)`——新到旧，`limit<=0` 不限
  - `AppendPolicyDefaults(ctx context.Context, d PolicyDefaults) (PolicyDefaults, error)`——版本号按 kind 递增（一个事务内 `COALESCE(MAX(version),0)+1`，`nextVersion` 现在按 `conversation_key` 写死，另写一个按 `kind` 的小查询），零 `CreatedAt` 用当前时间
  - `PolicyFollowers(ctx context.Context, convType string) ([]string, error)`——`conversations` 里 `type = convType` 且（`group_policies` 没有行，或版本号最大的那行 `follows_defaults = 1`）的 `key`，按 key 排序；一条 SQL
- 仅 sqlite（不进接口，照 `MigrateConfigBotSettings`）：`(*DB) InitPolicyDefaults(ctx context.Context, imports, builtins []store.PolicyDefaults, now time.Time) (imported []string, err error)`，一个事务：
  - `imports` 里任一 kind 在表里已有行 → 什么都不写，返回 `store.ErrAlreadyMigrated`；
  - 否则 `imports` 每行写成版本 1；`builtins` 里表中还没有行、且不在 `imports` 里的 kind 写成版本 1，已有的跳过；
  - `imports` 非空时写 app_meta `config_policy_defaults_migrated = now`；返回导入的 kind（按 `imports` 顺序）。
  - 行里的 `Reason`/`UpdatedBy` 由调用方给（T3 决定值）；零 `CreatedAt` 用 `now`。

- [ ] **Step 1: 写失败的测试**
  - `TestPolicyDefaults_AppendListLatest`：group 追加 3 版、private 追加 1 版 → group 版本 1、2、3，private 版本 1（两种 kind 各自计数）；`ListPolicyDefaults(group, 2)` 返回版本 3、2；`Latest(group)` 的 `Policy.Context.MaxRecentMessages` 等于第 3 版写入的 33；没写过的 kind → `errors.Is(err, store.ErrNotFound)`。
  - `TestPolicyVersions_FollowsDefaultsRoundTrip`：追加一版 `FollowsDefaults: true`，`LatestPolicy` 和 `ListPolicyVersions` 都读回 true；旧写法（不设）读回 false。
  - `TestPolicyFollowers`：建 group 会话 A（无策略）、B（单独一版）、C（单独一版后一版 follows）、D（follows 后又单独一版）和 private 会话 P（无策略）→ `PolicyFollowers(group)` = [A, C]，`PolicyFollowers(private)` = [P]。
  - `TestInitPolicyDefaults`：空库，imports=[group]、builtins=[group, private] → 返回 ["group"]，两种 kind 都是版本 1，group 的 `UpdatedBy`/`Reason` 是 imports 行的，meta 键存在；再调一次 imports=[private] → `ErrAlreadyMigrated`，表里仍各 1 行；另一空库只给 builtins → 两行写入、meta 不存在、返回空；builtins 对已有 kind 再调 → 跳过、无错。
- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/store/... -run 'PolicyDefaults|PolicyFollowers|FollowsDefaults|InitPolicyDefaults'` → 编译失败
- [ ] **Step 3: 实现**（按 Interfaces）
- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/store/...` → PASS（含现有迁移测试；如有断言最新迁移号的测试，改为 21）
- [ ] **Step 5: 检查并提交**：`gofmt -l . ; go vet ./internal/store/...`

```bash
git commit -am "feat(store): versioned policy defaults and a follows_defaults mark on conversation policies"
```

### Task 2: 策略服务——库里的默认值、热替换、跟随默认

**Files:**
- Modify: `internal/policy/policy.go`
- Modify: `pkg/conloomapi/policy.go`（新 `DefaultPrivatePolicy()`）
- Test: `internal/policy/policy_test.go`、`internal/policy/private_test.go`、新 `internal/policy/defaults_test.go`

**Interfaces:**
- `conloomapi.DefaultPrivatePolicy() GroupPolicy`：`DefaultGroupPolicy()` 把 `Memory.SummaryMaxChars`、`Memory.NoteMaxChars` 设 0（就是现在 `Config.PrivatePolicy()` 的回退规则作用在内置值上）。
- `const policy.KindGroup = conloomapi.ConvGroup`、`policy.KindPrivate = conloomapi.ConvPrivate`；`policy.KindOf(convKey string) string`（第三段是 private 时 `KindPrivate`，否则 `KindGroup`，与 `defaultsFor` 现有判断一致）；`policy.Builtin(kind string) conloomapi.GroupPolicy`（group → `DefaultGroupPolicy()`，private → `DefaultPrivatePolicy()`）。
- `Service` 内部改为每个 kind 一份 `store.PolicyDefaults`。`NewService(st, defaults)`、`WithPrivateDefaults(p)` 签名不变，给出版本 0 的内存初值（单元测试和 core 测试继续直接用；没调 `WithPrivateDefaults` 时 private 等于 group，同现在）。
- `(*Service) LoadDefaults(ctx context.Context) error`：两种 kind 各读 `LatestPolicyDefaults`；`ErrNotFound` 保留内存初值，其他错误返回。app 在构造后、任何读取前调用一次。
- `(*Service) Defaults(kind string) store.PolicyDefaults`（锁内拷贝；未知 kind 按 group）。
- `(*Service) SetDefaults(ctx context.Context, kind string, p conloomapi.GroupPolicy, reason, by, action string) (store.PolicyDefaults, error)`：
  - 未知 kind、`Validate(p)` 失败、`p.Context.MaxContextTokens` 在 `(0, reserved]` 内 → `ErrInvalid`（上下文检查抽成 `checkContextCap(p) error`，`Update` 同用，报错文字不变），什么都不写。`p` 按原样存，不补默认（补齐由面板解码时做，见 T4）。
  - 锁内：`AppendPolicyDefaults`、替换该 kind、`s.cache = map[string]store.PolicyVersion{}`（整个清空）、记下 `before`/`after` 和回调。
  - 锁外：审计 `settings.policy.update`（`Actor` = by，`Target` = kind，`BotInstanceID` 空，detail `{"kind","version","action"}`）；启动后台通知（见下）；审计失败与 `change` 一样连同已存版本一起返回 `policy: audit: …`。
- `(*Service) OnTransition(fn func(convKey string, before, after conloomapi.GroupPolicy))`：每次某会话的生效策略可能变了都调用，`before`/`after` 是生效策略（合并默认值后）。`change` 里：`before = cur.Policy`，追加后在锁内删缓存再 `current()` 得 `after`；先调 `OnChange` 再调 `OnTransition`。
- 后台通知（实施者决定：先返回、后台做）：`SetDefaults` 把一次通知放进 `s.wg` 管理的 goroutine，用 `s.fanMu` 串行化；`PolicyFollowers(ctx, kind)` 取会话，每批 100 个（`defaultsBatch = 100`），批间检查 `s.ctx`；对每个会话依次调所有 `OnChange(convKey)` 和 `OnTransition(convKey, before.Policy, after.Policy)`；列表失败只返回（无 logger，`Service` 不带日志；实施者决定：加 `WithLogger(*slog.Logger)` 记 Error，app 传 logger）。
- `(*Service) Wait()`（等进行中的通知结束，测试用）；`(*Service) Close()`（取消 `s.ctx` 后 `Wait`；app 关闭时最先调用，见 T3）。
- `current()`：库里没有 → `{ConversationKey, Policy: 该 kind 默认值, FollowsDefaults: true}`（版本 0，同现在）；最新版本 `FollowsDefaults` → 返回该版本的 Version/Reason/UpdatedBy/CreatedAt，`Policy` 换成该 kind 当前默认值，`FollowsDefaults: true`；否则照旧 `WithDefaults`，`FollowsDefaults: false`。
- `(*Service) FollowDefaults(ctx context.Context, convKey, reason, by string) (store.PolicyVersion, error)`：走 `change`，action `follow_defaults`；当前已跟随（`cur.FollowsDefaults`）→ 不写、返回当前；否则追加 `{Policy: 当前默认值快照, FollowsDefaults: true, Reason: reason（空时 "改回跟随默认"）}`。`change` 增加参数 `follows bool` 写进追加的版本；其余调用传 false。审计照旧 `policy.update` `{action, version}`。
- `Update`/`Pause`/`Resume`/`SetProactiveEnabled`/`SetPrivateLevel` 对跟随者照旧以 `cur.Policy`（= 默认值）为基础，写出的版本 `follows=false`，即变成单独设置。

- [ ] **Step 1: 写失败的测试**（假数据，sqlite 临时库，沿用 `policy_test.go` 的建库辅助函数）
  - `TestPolicy_LoadDefaultsFromStore`：库里 group v2（max_recent 33）、private 无 → `LoadDefaults` 后 `Defaults(group).Version==2`，未存过的 group 会话 `Current` 得 33、`FollowsDefaults` true；private 会话用内存初值。
  - `TestPolicy_SetDefaultsClearsCache`：先 `Current` 两个 group 会话（一个从未存、一个最新版 follows）填缓存 → `SetDefaults(group, 33)` → 两者 `Current` 立即是 33，未调 `Forget`；单独设置的会话（max_recent 40）仍为 40。
  - `TestPolicy_SetDefaultsNotifiesFollowersOnly`：group A（无策略）、B（单独）、C（follows），private P → `SetDefaults(group, memory 开)` 后 `Wait()` → `OnChange` 收到 [A, C]（顺序不限），`OnTransition` 对 A 的 before 记忆关、after 开；B、P 都没收到。
  - `TestPolicy_SetDefaultsRejectsInvalid`：`concurrency.max_interactive_requests=3`、`WithReservedContextTokens(4096)` 下上下文上限 2000、kind `"channel"` → 都 `errors.Is(err, ErrInvalid)`；`ListPolicyDefaults` 行数不变，`Defaults` 不变，回调没被调。
  - `TestPolicy_SetDefaultsAudits`：detail 解出 `{"kind":"group","version":2,"action":"update"}`，kind `settings.policy.update`；action `restore` 原样记录。
  - `TestPolicy_FollowDefaultsUnpauses`：默认并发 1；会话 `Pause` → `FollowDefaults(…, "", "panel:admin")` → 新版本 `FollowsDefaults` true、`Reason` "改回跟随默认"；`Current` 并发 1；`OnChange` 调用 1 次，`OnTransition` before 并发 0、after 1；审计 `policy.update` action `follow_defaults`；再调一次 → 不追加版本。
  - `TestPolicy_FollowerUpdateBecomesIndividual`：跟随者 `Update` max_recent 40 → `FollowsDefaults` false；之后 `SetDefaults(33)` → 它仍 40，`Wait` 后没收到通知。
  - `TestPolicy_TransitionOnUpdate`：`Update` 打开记忆 → `OnTransition` before 关、after 开。
  - `TestPolicy_CloseStopsFanOut`：`Close()` 后 `SetDefaults` 不阻塞、不 panic（通知被跳过）。
  - `TestDefaultPrivatePolicy`（conloomapi 包）：等于 `DefaultGroupPolicy()` 除两项字数上限为 0，且 `Validate()` 通过。
- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/policy/ ./pkg/conloomapi/` → 编译失败
- [ ] **Step 3: 实现**；更新 `Current`、`Update`、`OnChange`、`change` 的注释（`OnChange` 现在也在默认值改动时为跟随者调用）
- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/policy/ ./pkg/... ./internal/core/ ./internal/panel/` → PASS（后两个确认现有调用方不受影响）
- [ ] **Step 5: 检查并提交**：`gofmt -l . ; go vet ./...`

```bash
git commit -am "feat(policy): defaults loaded from the database, hot-swapped and fanned out to following conversations"
```

### Task 3: 配置旧键、启动导入、运行时接线、测试夹具

**Files:**
- Modify: `internal/config/config.go`（字段注释、`Load`、`PolicySections`、`PrivatePolicy` 注释）
- Modify: `internal/app/app.go`（`migratePolicyDefaults`、接线、`Close`）
- Modify: `internal/core/orchestrator.go`（:264、:358）
- Modify: `internal/memory/scheduler.go`（`PolicyChanged` 改签名）、`internal/memory/consolidate.go`（两个 Warn 函数）
- Modify: 测试夹具：`internal/app/e2e_test.go`（`configTemplate`、`testConfig`）、其他在同一数据目录重启且 YAML 带这两段的测试、`internal/config/config_test.go:686-689`、`internal/memory/cons_fit_test.go`、`internal/memory/private_test.go`、调用 `sched.PolicyChanged` 的 memory 测试
- Test: 新 `internal/app/policydefaults_e2e_test.go`、`internal/core/` 里新测试、`internal/memory/scheduler_test.go`（或现有 jump 测试所在文件）、`cmd/conloom` 现有退出码测试

**Interfaces:**
- 配置（实施者决定：保留 Go 字段名，避免约 40 个测试文件改动）：`DefaultGroupPolicy`、`DefaultPrivatePolicy` 的 yaml 键不变，注释改为"旧键：只在第一次启动导入数据库（见 app.New），运行时不读；文件没有这一段时是内置值，Go 里构造的配置（测试）可以改它作为空库的初值"。`Load` 在文件没有 `default_private_policy` 时把 `DefaultPrivatePolicy` 设为 `&conloomapi.DefaultPrivatePolicy()`（不再从文件的群聊段回退；有该段时照旧在内置群聊值上解码）。新 `(*Config) PolicySections() (group, private bool)`：文件里这两个键是否存在且非 null（`Load` 里用 `mappingValue` 记到未导出字段）。校验 `validate.go:265-270` 不变（内置值必然通过）。`PrivatePolicy()` 保留（Go 构造的配置 `DefaultPrivatePolicy` 为 nil 时仍按旧规则回退）。
- `app.migratePolicyDefaults(ctx context.Context, st *sqlite.DB, cfg *config.Config, now time.Time, loc *time.Location, logger *slog.Logger) error`，在 `app.go:171` 构造 `policies` 之前调用：
  - imports：`PolicySections()` 为真的段，`{Kind, Policy: 该段值, Reason: "从配置文件导入", UpdatedBy: "system"}`；builtins：group `cfg.DefaultGroupPolicy`、private `cfg.PrivatePolicy()`，`Reason: "内置默认值"`、`UpdatedBy: "system"`（`Load` 过的配置这两个就是内置值；实施者决定）。
  - `InitPolicyDefaults` 返回 `ErrAlreadyMigrated` → 返回 `fmt.Errorf("数据库里已经有默认策略（%s），群聊、私聊默认策略现在只在 Web 管理面板的设置页里改；请删除配置文件里的 %s，然后再启动（%w）", when, 段名用"、"连接, err)`，`when` 为 `migratedWhen(...)+"从配置文件导入"`（有 meta 时）或 `"第一次启动时写入的内置默认值"`（无 meta）。
  - 导入了 → 每段一条 Info 日志；审计 `settings.policy.migrate`（Actor `system`，detail `{"imported":["group",…]}`，失败只记 Error）；Warn 日志 "the config file's default policies are now in the database and edited in the web panel's settings page; remove default_group_policy and default_private_policy from the config file, or the next start fails"。
- 接线（app.go）：`policies := policy.NewService(...).WithPrivateDefaults(...).WithReservedContextTokens(...).WithLogger(logger)`，紧接 `policies.LoadDefaults(ctx)`（失败走关库返回）；`policies.OnChange(sched.PolicyChanged)` 改为 `policies.OnTransition(sched.PolicyChanged)`；`App.Close()` 第一步 `policies.Close()`（`App` 结构体加字段保存它）。
- `memory.(*Scheduler).PolicyChanged(convKey string, before, after conloomapi.GroupPolicy)`：`!memoryOn(before) && memoryOn(after)` 时标记 jump，不再读 `ListPolicyVersions`（"没有记忆段"已由 `current` 合并默认值体现）。注释同步。
- `memory.WarnOutputReservation(cfg *config.Config, group, private conloomapi.GroupPolicy, log *slog.Logger) string`、`memory.WarnPrivateMemoryNotSensitive(cfg *config.Config, private conloomapi.GroupPolicy, log *slog.Logger) string`：条件同现在但读参数；照旧写英文日志，并返回给面板显示的中文提示（无问题返回 ""）：
  - `长模型（long_context）给输出预留的令牌数 %d 低于建议的 %d，记忆整理时摘要和笔记会比上限短`
  - `私聊记忆已打开，但长模型（long_context）不允许处理敏感内容（privacy.allow_sensitive_content），私聊和面板聊天不会整理记忆`
  app.go:195-196 改传 `policies.Defaults(KindGroup).Policy`、`Defaults(KindPrivate).Policy`。
- `core`：:264 改读 `o.policies.Defaults(policy.KindGroup).Policy.Review.Level`；:358 改为 `o.policies.Defaults(policy.KindOf(convKey)).Policy`。`Orchestrator.PolicyChanged` 不变（`OnChange` 已覆盖默认值改动）。
- 测试夹具（实施者决定）：e2e `configTemplate` 删掉 `default_group_policy` 段，`testConfig` 改为在 Go 里把同样的参数写进 `cfg.DefaultGroupPolicy`（空库按 builtins 规则写入，所以重启同一数据目录不会 78）；其他 YAML 带这两段且会二次 `app.New` 的测试同样处理；只启动一次的（gateway、client、适配器只解析配置）不动。全量测试找漏网的。

- [ ] **Step 1: 写失败的测试**
  - `TestPolicyDefaults_ImportOnFirstStart`（app）：YAML 两段都有（group `memory.enabled: true`；private `reply.max_parts: 5`）→ `app.New` 后 `LatestPolicyDefaults(group)` 版本 1、`UpdatedBy` system、`Reason` 从配置文件导入、记忆开；private `MaxParts==5`；meta `config_policy_defaults_migrated` 存在；`settings.policy.migrate` 审计 1 条，detail imported = ["group","private"]。
  - `TestPolicyDefaults_FreshInstallSeedsBuiltins`：两段都没有 → group v1 等于 `conloomapi.DefaultGroupPolicy()`，private v1 等于 `DefaultPrivatePolicy()`；无 meta、无 migrate 审计。
  - `TestPolicyDefaults_OnlyGroupSection`：只有 group 段且 `context.max_recent_messages: 33`、`memory.summary_max_chars: 1500` → group 导入；private 等于 `DefaultPrivatePolicy()`（max_recent 不是 33、摘要上限 0）；imported = ["group"]。
  - `TestPolicyDefaults_RestartWithKeysFails`：上一个场景关掉后用同一数据目录、同一文件再 `app.New` → `errors.Is(err, store.ErrAlreadyMigrated)`，消息含 `default_group_policy` 和 `设置页`、含"已在 "；库里仍是各 1 版。子用例：先无键启动（内置值）、再加上 private 段启动 → 同样失败，消息含 `内置默认值` 和 `default_private_policy`。
  - `TestPolicyDefaults_RestartWithoutKeysUsesDB`：首启导入后在 Go 里 `SetDefaults(group, 33)`，关掉、去掉两段重启 → `Current` 未存过策略的群会话为 33。
  - cmd：在现有 `ErrAlreadyMigrated` 退出码测试旁加一例，包装后的默认策略错误 → 78。
  - `TestOrchestrator_DefaultsChangePausesFollowers`（core）：群 A 跟随默认、群 B 单独设置并发 1，两边各有排队任务 → `SetDefaults(group, 并发 0)` + `Wait()` → A 的任务被取消、B 的不变；再 `SetDefaults` 回并发 1 → A 被 `Kick`（新消息能得到回复）。
  - `TestScheduler_TransitionTurningMemoryOnJumps`（memory）：`PolicyChanged(key, 关, 开)` → jump 被标记；`(开, 开)`、`(开, 关)` 不标记。
  - 更新 `TestWarnOutputReservation`、`TestWarnPrivateMemoryNotSensitive`：新签名，另断言返回的中文提示非空/为空；`config_test.go:686` 改断言为 `DefaultPrivatePolicy` 等于 `DefaultPrivatePolicy()` 且 `PolicySections()` 为 (false, false)；加 `TestLoad_PolicySections`（只写 group 段 → (true, false)，private 为内置值不随 group 段变）。
- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/app/ ./internal/config/ ./internal/memory/ ./internal/core/ -run 'PolicyDefaults|PolicySections|DefaultsChange|Transition|Warn'` → FAIL
- [ ] **Step 3: 实现**（按 Interfaces），顺带更新 `migrateBotSettings` 附近的说明风格注释、`redactedConfig` 以外的注释里"配置文件的 default_*_policy"说法
- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...` → PASS（全量，夹具问题在这里暴露）
- [ ] **Step 5: 检查并提交**：`gofmt -l . ; go vet ./...`

```bash
git commit -am "feat(app): import default policies into the database on first start and apply default changes live"
```

### Task 4: 面板接口——设置、改回跟随默认、配置页数据来源

**Files:**
- Create: `internal/panel/settings.go`（三个处理函数、视图类型、`policyWarnings`）
- Modify: `internal/panel/write_routes.go`、`internal/panel/data_routes.go`（GET 放读路由处，照 :23）
- Modify: `internal/panel/writes.go`（`followDefaults`；`restorePolicy` 处理 follows 版本）
- Modify: `internal/panel/views.go`（`policyVersionView` 加字段）、`internal/panel/readviews.go`（`getPolicy` 加 `defaults`）
- Modify: `internal/panel/configview.go`（两段改读服务）
- Test: 新 `internal/panel/settings_test.go`、`writes_test.go`、`readviews_test.go`、`configview_test.go`
- Docs: `docs/api.md` 若列面板接口则补（否则 T6 的 `docs/panel.md` 写）

**Interfaces:**
- `GET /panel/api/settings/policies` → `200 {"group": section, "private": section}`，`section = {"current": defaultsView, "versions": [defaultsView…≤50], "followers": int, "memory_limits": memoryLimitsView}`；`defaultsView = {version, policy, reason, updated_by, created_at}`（`created_at` 用 `nullTime`，版本 0 时 null）。`followers = len(PolicyFollowers(kind))`；`memory_limits` 用 `memory.SummaryLimit/NoteLimit(current.policy.memory, kind)` 与类型默认，形状同会话策略页。
- `PUT /panel/api/settings/policies/{kind}` 请求 `{policy, reason}`：解码前把 `req.Policy` 预置为 `policy.Builtin(kind)`，缺的字段按内置值补齐，与配置文件在内置值上解码的规则一致（实施者决定）。未知 kind → 404 `CodeNotFound`；`ErrInvalid` → 400（`respond.go:96` 已映射，消息里带 `Validate` 的字段路径）。成功 → `200 {"current": defaultsView, "warnings": [string]}`（无警告时 `[]`，不是 null）；action `update`，actor `panelActor(sess)`。
- `POST /panel/api/settings/policies/{kind}/restore` 请求 `{version, reason}`：`version<1` → 400；不存在 → 404；`reason` 空时 `恢复到第 N 版`；把旧版 policy 原样传给 `SetDefaults(..., "restore")`；返回同 PUT。
- `policyWarnings(kind string, p conloomapi.GroupPolicy) []string`：用保存后的两段默认值调 T3 的两个 Warn 函数（只收非空）；`!review.LevelImplemented(p.Review.Level)` 时加 `审查级别 %q 还没有实现，目前按 basic 处理`，并 `s.log.Warn`。
- `POST /panel/api/conversations/{key}/policy/follow-defaults` 请求 `{reason}` → `Policies.FollowDefaults` → `s.writePolicy`（`200 {"current": policyVersionView}`，实施者决定：和 PUT/restore 一致）。
- `policyVersionView` 加 `FollowsDefaults bool json:"follows_defaults"`；`getPolicy` 响应加 `"defaults": {"kind": "group"|"private", "version": N}`（`Policies.Defaults(KindOf(key)).Version`）。
- `restorePolicy`：被恢复的版本 `FollowsDefaults` 时改调 `FollowDefaults`（实施者决定：恢复"跟随默认"那一版就是重新跟随）。
- `redactedConfig` 签名加 `defaults func(kind string) store.PolicyDefaults`（或直接传 `*policy.Service`）：两个键的值改为 `{"source": "panel", "version": N, "policy": {...}}`；注释同步（不再是"配置文件的值"）。

- [ ] **Step 1: 写失败的测试**（沿用 `storeharness_test.go`/`writes_test.go` 的服务器、登录、CSRF 辅助）
  - `TestSettingsPolicies_Get`：group 写 55 版、一个跟随的群会话、一个单独设置的群会话 → `group.versions` 长 50、首个是第 55 版；`group.followers==1`；`private.current.version` 正确；`memory_limits` 四个字段齐全。
  - `TestSettingsPolicies_Put`：PUT group `{policy:{context:{max_recent_messages:33}}, reason:"调小"}` → 200，`current.version` 递增，存下的 policy 除 33 外等于 `conloomapi.DefaultGroupPolicy()`，`warnings` 是 `[]`；审计 `settings.policy.update` detail action update；随后 GET 跟随者的 `…/policy` → `current.policy.context.max_recent_messages==33`、`follows_defaults` true。
  - `TestSettingsPolicies_PutInvalid`：并发 3 → 400，错误消息含 `concurrency.max_interactive_requests`；kind `channel` → 404；版本数不变。无 CSRF 头 → 403。
  - `TestSettingsPolicies_PutWarnings`：long 模型 `allow_sensitive_content: false`，PUT private 记忆开 → `warnings` 有一条含 `敏感内容`；review level 设为未实现的级别 → 含 `审查级别`。
  - `TestSettingsPolicies_Restore`：v1→v2 后 restore v1 → v3 的 policy 等于 v1，审计 action restore；version 0 → 400；version 99 → 404。
  - `TestFollowDefaults_Endpoint`：单独设置且暂停的群会话 → 200，`current.follows_defaults` true、并发等于默认值；审计 `policy.update` detail action `follow_defaults`；GET `…/policy` 的 `defaults.kind=="group"`、`defaults.version` 等于当前默认版本。
  - `TestRestorePolicy_FollowsVersionFollowsAgain`：恢复一个 follows 版本 → 新版本 `follows_defaults` true。
  - `TestConfigView_DefaultPoliciesFromDB`：`SetDefaults(group, 33)` 后 GET `/panel/api/config` → `default_group_policy.source=="panel"`、`version` 对、`policy.context.max_recent_messages==33`；`TestConfigView_PassThroughTypesHaveNoSecretFields` 仍通过。
- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/ -run 'Settings|FollowDefaults|FollowsVersion|ConfigView'` → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/... ./internal/app/` → PASS
- [ ] **Step 5: 检查并提交**：`gofmt -l . ; go vet ./internal/panel/...`

```bash
git commit -am "feat(panel): settings API for default policies and follow-defaults for conversations"
```

### Task 5: 面板界面——共用策略表单、设置页、导航、会话页状态

**Files:**
- Create: `internal/panel/web/views/policyform.js`、`internal/panel/web/views/settings.js`
- Modify: `internal/panel/web/views/conversation.js`（`policyTab` 改用共用模块，加状态和按钮）、`views/config.js`（提示和两段显示）、`lib/sections.js`、`app.js`（注册路由）
- Test: `internal/panel/web/web_test.go`（含 `TestAssets_PlannedFilesPresent` 列表）

**Interfaces:**
- `policyform.js` 导出（从 `conversation.js:158-279` 搬出，行为、文案、校验提示不变；`select`、`field`、`fail`、`write`、`PRIVATE_LEVEL_CHOICES`、`CLOCK` 若是 conversation.js 私有的一起搬过去并导出）：
  - `policyForm({ policy, isPrivate, limits, onSave })` → `<form>`：常用项（主动发言、记忆、历史条数、分条上限、摘要/笔记字数；私聊另有私聊等待、私聊最长等待、主动找人档位、免打扰开始/结束）+ 原因 + 保存；`onSave(policy, reason)` 返回 Promise，成功文案由调用方给（`okText`）。
  - `policyJSONForm({ policy, hint, onSave })` → JSON 编辑表单（会话页 hint 原文不变；设置页 hint `整份替换默认策略；没写的字段按内置默认值补齐。`）。
  - `policyVersions({ versions, current, onRestore, label })` → 历史版本表，"恢复此版本"按钮；`label(v)` 让会话页给 follows 版本加 `（跟随默认）`。
- `settings.js` 导出 `settingsView(ctx)`：`h1` `设置`；提示 `默认策略保存后立即生效，不用重启。`；两块 `群聊默认策略`、`私聊默认策略`（实施者决定的标题），每块：顶部一句 `当前有 ${followers} 个会话跟随这个默认值；单独改过策略的会话不受影响。`、`当前：第 N 版`（修改人、原因、时间）、`policyForm`、`<details>` 摘要 `高级：JSON 编辑` 内 `policyJSONForm`、`policyVersions`。保存调 `PUT settings/policies/<kind>`；成功显示 `已保存`，`warnings` 非空时逐条显示在警告框（沿用现有警告样式类），然后重新加载本页。恢复用 `prompt` 同会话页文案，调 `…/restore`。
- `sections.js`：在 `['config', '配置']` 前加 `['settings', '设置']`；`app.js` 加 `route('/settings', settingsView)`。
- 会话策略页：第一张卡片标题下显示 `跟随默认（群聊默认 第 N 版）`/`跟随默认（私聊默认 第 N 版）`（来自 `res.defaults`）或 `单独设置（第 N 版）`；单独设置时显示按钮 `改回跟随默认`，确认框：`改回后，这个会话改用${群聊|私聊}默认策略，单独设置的内容不再生效（历史版本保留）。` 暂停中（`max_interactive_requests === 0`）时再接一句 `当前是暂停状态，改回后会按默认值取消暂停。` 最后 `确定改回？`；确认后 `POST …/policy/follow-defaults` `{reason:'改回跟随默认'}`，flash `已改回跟随默认`，刷新。跟随默认时加一行提示 `修改下面的设置会让这个会话改为单独设置。`
- `config.js`：提示改为 `当前生效的配置，只读。密钥类字段只显示是否已设置；默认策略（default_group_policy、default_private_policy）来自数据库，在设置页修改；其余配置需要改文件后重启。`；这两行的值旁显示链接 `在设置页修改`（`#/settings`）。

- [ ] **Step 1: 写失败的测试**（静态检查，沿用 `web_test.go` 写法）
  - `TestSettingsView`：`views/settings.js` 含 `settings/policies`、`个会话跟随这个默认值；单独改过策略的会话不受影响。`、`恢复此版本`（或经 `policyVersions`）、`高级：JSON 编辑`、`warnings`、`群聊默认策略`、`私聊默认策略`，并 import `./policyform.js`；`app.js` 含 `route('/settings', settingsView)`。
  - `TestSections_Order`：解析 `lib/sections.js` 的标题顺序恰为 概览、用量、聊天、会话、人设模板、任务、审计、设置、配置。
  - `TestPolicyForm_Shared`：`policyform.js` 含 `分条上限`、`免打扰开始`、`export function policyForm`、`export function policyJSONForm`；`conversation.js` import `./policyform.js` 且不再含 `field('分条上限'`（防止复制）。
  - `TestConversationPolicy_FollowDefaults`：`conversation.js` 含 `改回跟随默认`、`policy/follow-defaults`（或 `follow-defaults`）、`跟随默认（`、`单独设置（`、`改回后会按默认值取消暂停`。
  - `TestConfigView_SettingsHint`：`config.js` 含 `在设置页修改`，不再含 `修改配置需要改文件后重启。`
  - `TestAssets_PlannedFilesPresent` 加两个新文件；`TestAssets_ImportsResolve`、`TestAssets_NoAPIsNewerThanChrome89` 自动覆盖新文件。
- [ ] **Step 2: 运行确认失败**：`go test ./internal/panel/web/ -run 'SettingsView|Sections_Order|PolicyForm|FollowDefaults|SettingsHint|PlannedFiles' -count=1` → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/...` → PASS；`for f in internal/panel/web/views/{policyform,settings,conversation,config}.js internal/panel/web/{app.js,lib/sections.js}; do /usr/local/node/bin/node --check "$f"; done` → 无输出
- [ ] **Step 5: 自测**：在 `/tmp` 写一次性假 DOM 脚本（不提交），对 `policyForm` 喂假的群聊、私聊策略，提交后检查 `onSave` 收到的 policy 只改了表单字段；结果写进报告
- [ ] **Step 6: 检查并提交**：`gofmt -l . ; go vet ./internal/panel/...`

```bash
git commit -am "feat(panel): settings page for default policies and follow-defaults on the conversation page"
```

### Task 6: 文档和示例配置

**Files:**
- Modify: `conloom.example.yaml`、`docs/configuration.md`、`docs/deployment.md`、`docs/panel.md`、`docs/usage.md`、`docs/api.md`、`docs/architecture.md`
- Test: `internal/config/config_test.go`（读示例配置的测试，:322 等）

**内容**（设计第 7 节）：
- `conloom.example.yaml`：删掉 `default_group_policy`、`default_private_policy` 两段，原位置留注释 `# default_group_policy、default_private_policy 已搬到面板的设置页；旧文件第一次启动时会自动导入，之后要删掉这两段。`。读示例配置取 `DefaultGroupPolicy` 的测试（`config_test.go:322` 等）改为断言等于内置值，或改读测试自带的 YAML。
- `configuration.md`：字段说明保留，开头改为"在面板设置页修改"；写导入的四种情况、两段分开判断、`config_policy_defaults_migrated`、`settings.policy.migrate`、退出码 78；明确"私聊默认策略不再在缺省时回退到群聊默认，两段在数据库里各自独立"；面板 PUT 缺的字段按内置值补齐。
- `deployment.md`：升级说明——备份、第一次启动导入（迁移版本到 21）、核对设置页、删掉两段、重启；只写步骤和假示例，不写线上值。
- `panel.md`：设置页（两块、跟随数、保存即生效、warnings、版本恢复）、导航顺序、会话策略页两种状态和"改回跟随默认"（暂停会被取消）、配置页 `source: panel`、四个新接口和审计类型。
- `usage.md`、`api.md`、`architecture.md`：`grep -n "default_group_policy\|default_private_policy\|default_\*_policy"` 逐处改成"面板设置页"；`architecture.md` 补 `policy_defaults`、`follows_defaults`、`SetDefaults` 的后台通知和 `OnTransition`。

- [ ] **Step 1: 改示例配置并让读它的测试先失败再修**：`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/config/` → 先 FAIL（示例配置不再有段）→ 改测试 → PASS
- [ ] **Step 2: 改文档**；`grep -rn "default_group_policy\|default_private_policy" docs conloom.example.yaml README.md` 只剩"旧键/导入"语境
- [ ] **Step 3: 全量检查并提交**：`gofmt -l . ; go vet ./... ; CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...` → PASS

```bash
git commit -am "docs: default policies are edited in the panel's settings page"
```

---

## Self-review

- **设计覆盖**：存储（3 节）→ T1；四种启动情况、两段分开判断、退出码 78、旧键校验（4 节）→ T3；`SetDefaults` 清缓存、通知跟随者、比较改前改后、后台分批、校验、保存时警告、改回跟随默认（5 节）→ T2/T3/T4；接口、审计、导航、设置页布局、会话页状态、配置页（6 节）→ T4/T5；文档（7 节）→ T6。第 8 节是部署者的事，不进计划。私聊语义变化 → T3 `Load` + T6 文档。
- **类型一致**：kind 字符串处处是 `group`/`private`（`policy.KindGroup/KindPrivate` = `conloomapi.ConvGroup/ConvPrivate`，也是 `PolicyFollowers` 的 `convType`）；`store.PolicyDefaults` 在 T1 定义，T2 `Defaults`/`SetDefaults` 返回它，T4 `defaultsView` 由它转换；`OnTransition` 签名与 `Scheduler.PolicyChanged` 新签名相同，可直接注册。
- **步骤检查**：每个任务都有先失败的测试、确认失败、实现、确认通过、提交；T3 是唯一必须跑全量的中间任务（夹具改动）。
- **比例**：默认值热替换只动 `policy.Service` 一处，其他包继续通过 `Current` 读；不新加跨包接口，只加两个回调注册点之一（`OnTransition`）。

## 实施者决定（汇总）

1. 保留 Go 字段名 `DefaultGroupPolicy`/`DefaultPrivatePolicy`（yaml 键不变）作为导入源和空库初值，用 `PolicySections()` 判断文件里有没有，而不是改成指针新字段——避免约 40 个测试文件改动。
2. 空库且文件没有某段时，写入的是 `cfg` 里的值；`Load` 过的配置这就是内置值（私聊 `DefaultPrivatePolicy()`），Go 构造的测试配置可借此设初值。
3. 通知跟随者：`SetDefaults` 先返回，后台 goroutine 每批 100 个依次调 `OnChange` 和新回调 `OnTransition`；记忆调度器两条路径都改用改前/改后生效策略比较（不再比较版本号）。
4. "跟随默认"版本存当时默认值的快照，便于看历史；恢复这种版本 = 重新跟随。
5. 面板 PUT 的 policy 在内置值上解码，缺的字段按内置值补齐；`SetDefaults` 本身按原样校验和存储。
6. follow-defaults 接口返回 `{current}`（同 PUT/restore）；会话策略 GET 加 `defaults: {kind, version}`。
7. 内置值行的 reason 为 `内置默认值`；设置页两块标题 `群聊默认策略`、`私聊默认策略`；确认框、提示等文案见 T5。
8. `policy.Service` 加 `WithLogger`，后台通知出错只记日志。
