# ConLoom 织语

让对话沉淀为记忆，让记忆塑造人格。

ConLoom 是一个用 Go 编写的聊天机器人核心：平台适配器（Adapter）把聊天消息通过标准 REST API 交给核心，核心负责回复判定、上下文组装、模型调用、审查和可靠投递。单个二进制、无 cgo、无需数据库或 Docker，Linux／macOS／Windows × amd64／arm64 开箱即用。

## 功能（P1）

- 标准 Bot Interaction API（REST）：事件提交、任务查询与取消、Outbox 长轮询拉取与 ACK、能力协商、人格与群策略读写。
- 两个 Adapter，都只通过公开 API 与核心交互：NapCat（OneBot 11 正向 WebSocket，真实 QQ 平台）与 Mock（终端里本地对话）。
- 可靠投递：幂等事件、持久化 Outbox、至少一次投递 + Adapter 端按 `action_id` 本地去重；重启后恢复未完成任务，过时的回复不再发送。
- 回复策略：私聊、@Bot、回复 Bot、命令触发；同一发送者连续短消息合并为一次回复；每用户限流、每会话最短回复间隔；核心与 Adapter 两层自回复防护。
- 人格：内置不可变核心人格 + 可配置的群人格模板，人格与群策略都有版本，可修改、可回滚。
- 模型：任意 OpenAI 兼容接口，按 `short_context`／`long_context` 角色路由，换模型只改配置；熔断与降级。
- 并发准入：模型全局并发、群并发（0／1／2）、token 预留预算、优先级队列与队列上限。
- 基础审查（不可关闭）：控制字符清理、平台控制码剥离、密钥打码、屏蔽词。
- 可观测：`log/slog` 结构化日志、trace_id、Prometheus 指标。

## 路线

- P2：长期记忆（混合检索、向量、滚动摘要、长期事实、`/remember` `/forget`、分层遗忘）、人格观察与自动演化、PostgreSQL／pgvector／Redis 存储实现。
- P3：Tool Gateway、Web 搜索、ComfyUI 等工具能力。
- P4：多实例水平扩展、多 Agent、流式输出等进阶能力（规划中，以届时设计为准）。

## 架构

```text
QQ 群 ⇄ NapCat ⇄(OneBot 11 正向 WS)⇄ conloom adapter napcat ⇄(REST /api/v1)⇄ conloom serve ⇄ 模型网关（OpenAI 兼容）
                                                                             │
                                                              <data_dir>/conloom.db（SQLite）
```

一条消息的处理过程：

1. Adapter 把平台消息转为标准 Event，`POST /api/v1/events`，带 `Idempotency-Key`。
2. Gateway 校验 token 权限、Schema 与幂等键，丢弃 Bot 自身的消息。
3. 消息先落库，再由回复策略判定 `reply`／`observe`／`ignore`。
4. `reply`：进入该会话的串行执行单元，在合并窗口内收集同一发送者的连续短消息。
5. 加载人格版本、群策略与近期消息，组装上下文并估算 token。
6. 角色路由选出模型 Profile，准入控制放行后调用模型。
7. 输出经基础审查与后处理写入 Outbox，并唤醒长轮询中的 Adapter。
8. Adapter 拉取、发送、ACK；核心记录 Bot 自己的回复及其来源关系。

## 安装

**预编译二进制（推荐）**：在 [Releases](https://github.com/kinako404/conloom/releases) 下载对应平台的压缩包 `conloom_<版本>_<系统>_<架构>.tar.gz`（Windows 为 `.zip`），解压即可，不需要任何运行时。可用同页的 `checksums.txt` 校验：

```sh
sha256sum -c checksums.txt --ignore-missing
```

压缩包内含 `conloom` 二进制、`README.md`、`LICENSE`、`conloom.example.yaml` 与 `deploy/`。macOS 首次运行若被拦截，可执行 `xattr -d com.apple.quarantine ./conloom`。

**从源码安装**：需要 Go ≥ 1.26（由 SQLite 纯 Go 驱动的要求决定）。

```sh
go install github.com/kinako404/conloom/cmd/conloom@latest
```

## 快速开始

1. 复制示例配置：

   ```sh
   cp conloom.example.yaml conloom.yaml
   ```

2. 在 `conloom.yaml` 同目录新建 `.env`（`chmod 600 .env`），填入真实值。示例配置里的 `${VAR}` 会从进程环境变量或这个 `.env` 中展开（进程环境变量优先）：

   ```sh
   CONLOOM_ADAPTER_TOKEN=<随机长字符串>
   CONLOOM_ADMIN_TOKEN=<另一个随机长字符串>
   CONLOOM_BOT_SELF_ID=<Bot 的 QQ 号>
   CONLOOM_ADMIN_QQ=<管理员 QQ 号>
   CONLOOM_GROUP_ID=<启用的群号>
   CONLOOM_SHORT_MODEL_ENDPOINT=https://<你的模型网关>/v1
   CONLOOM_SHORT_MODEL_NAME=<短上下文模型名>
   CONLOOM_SHORT_MODEL_KEY=<API Key>
   CONLOOM_LONG_MODEL_ENDPOINT=https://<你的模型网关>/v1
   CONLOOM_LONG_MODEL_NAME=<长上下文模型名>
   CONLOOM_LONG_MODEL_KEY=<API Key>
   CONLOOM_NAPCAT_WS_URL=ws://<NapCat 地址>:<端口>
   CONLOOM_NAPCAT_TOKEN=<NapCat access token>
   ```

   模型请求发往 `<endpoint>/chat/completions`，所以 endpoint 一般写到 `/v1` 为止。

3. 启动核心服务（默认监听 `127.0.0.1:8787`）：

   ```sh
   ./conloom serve -c conloom.yaml
   curl http://127.0.0.1:8787/healthz   # 返回 ok
   ```

4. 另开一个终端，用 Mock Adapter 在本地对话：

   ```sh
   ./conloom mock -c conloom.yaml                                  # 以第一个管理员的身份私聊
   ./conloom mock -c conloom.yaml -conv group:<群号> -user <QQ号>:<昵称>   # 模拟群聊
   ```

   每行输入一条消息；在群聊里以 `@` 开头表示 @Bot。按 Ctrl+D（Windows 为 Ctrl+Z 回车）退出。

5. 接入 QQ：在 NapCat 中开启 OneBot 11 正向 WebSocket 服务并设置 access token，然后启动 NapCat Adapter：

   ```sh
   ./conloom adapter napcat -c conloom.yaml
   ```

   之后在启用的群里 @Bot 即可聊天。注意：Mock 与 NapCat Adapter 默认使用同一个 Bot 实例，会争抢同一个 Outbox；P1 假设每个 Bot 实例只有一个 Adapter 进程，不要同时运行两者。

## 命令行

```text
conloom serve -c conloom.yaml                                      核心服务（API + 编排 + Worker）
conloom adapter napcat -c conloom.yaml                             NapCat Adapter（独立进程，可部署在别的机器）
conloom mock -c conloom.yaml [-conv group:<群号>] [-user <QQ号>:<昵称>]   终端里的 Mock Adapter
conloom backup -c conloom.yaml -o <文件>                           在线备份数据库（目标文件必须不存在）
conloom version                                                    显示版本与提交
```

`-c` 默认为 `conloom.yaml`。退出码：0 正常，1 运行时错误，2 参数或配置错误。收到 SIGINT／SIGTERM 时优雅退出。

## 聊天命令

| 命令 | 权限 | 作用 |
|---|---|---|
| `/help` | 所有人 | 简短说明 |
| `/stop` | 发起者或管理员 | 取消本会话正在进行或排队的生成 |
| `/persona` | 所有人 | 显示当前人格名称与版本 |
| `/pause`、`/resume` | 管理员 | 暂停本群回复（群并发设为 0）／恢复 |

命令前缀默认为 `/`（群策略 `reply.command_prefix`）。管理员只认配置中 `bots[].admins` 列出的平台用户 ID。

## 配置

单一 YAML 文件，完整带注释的示例见 [`conloom.example.yaml`](conloom.example.yaml)。支持 `${VAR}` 与 `${VAR:-默认值}` 展开，`$$` 表示字面量 `$`；未知字段会被拒绝，启动时严格校验并给出可读错误。真实的 `conloom.yaml` 与 `.env` 已被 `.gitignore` 排除，不要提交它们。

| 段落 | 说明 |
|---|---|
| `server` | `listen`（默认 `127.0.0.1:8787`）、`timezone`（展示用时区，默认 `Asia/Shanghai`）、`metrics_enabled`（是否开放 `/metrics`，默认开） |
| `storage` | `data_dir`：数据目录，默认 `./data`，相对路径按配置文件所在目录解析 |
| `api_tokens` | API 令牌列表：`name`、`token`、绑定的 `bot_instance_id`、`scopes`。Adapter 令牌通常只需 `events:write tasks:read outbox:read outbox:ack persona:read`；管理令牌另加 `memory:read memory:write policy:write admin:read` |
| `bots` | Bot 实例：`id`、`platform`、`name`（显示名，默认“织语”）、`platform_self_id`（Bot 自身的平台 ID，必填，用于自回复防护）、`admins`、`enabled_conversations`（只在这些会话工作）、`private_chat`（`admins`／`all`／`off`，默认 `admins`）、`persona_template` |
| `model_profiles` | 按角色配置模型，`short_context` 必需、`long_context` 可选：`provider`（`openai_compatible`）、`endpoint`、`model`、`api_key`、`modalities`、`context`（窗口与预算，须满足 `soft_input_budget <= configured_window <= native_window`）、`runtime`（并发、超时、重试）、`privacy`、`extra_body`（原样合并进请求体）、`token_estimate_factor` |
| `persona_templates` | 群人格模板：`style` 各项 0~1，`behavior` 为枚举；省略 `default` 时使用内置默认人格 |
| `default_group_policy` | 新会话的初始群策略：`concurrency`、`reply`（命令前缀、连续对话窗口、合并窗口 `debounce`、最短回复间隔、每用户限流）、`context`、`review`。通过 API 或命令修改后存库并版本化，库内版本优先 |
| `runtime` | `action_ttl`、`outbox_lease`、`stale_task_after`、`idempotency_retention`、`max_merge_window`、`session_idle` |
| `adapters.napcat` | `ws_url`、`access_token`、`core_url`、`core_token`、`bot_instance_id`、`state_dir`（去重记录目录，默认同 `data_dir`） |
| `adapters.mock` | `core_url`、`core_token`、`bot_instance_id` |
| `observability` | `log_level`、`log_format`（`json`／`text`）、`log_message_content`（默认 `false`） |

配置中的群号、QQ 号一律写平台原始 ID。

## API

所有 `/api/v1` 请求带 `Authorization: Bearer <token>`；令牌只能操作其绑定 Bot 实例下的会话、任务和 Outbox。

| 方法与路径 | 所需 scope | 说明 |
|---|---|---|
| `POST /api/v1/events` | `events:write` | 提交事件，必须带 `Idempotency-Key`；返回 202 与 `disposition` |
| `GET /api/v1/tasks/{task_id}` | `tasks:read` | 查询任务 |
| `POST /api/v1/tasks/{task_id}/cancel` | `tasks:read` | 取消任务 |
| `GET /api/v1/outbox?limit=&wait=` | `outbox:read` | 拉取待发送动作（长轮询，`wait` 如 `30s`，最长 30s） |
| `POST /api/v1/outbox/{action_id}/ack` | `outbox:ack` | 确认动作：`{"status":"sent|unsupported|failed","platform_message_id":"…"}` |
| `GET /api/v1/capabilities` | 任意有效令牌 | 能力协商 |
| `GET /api/v1/conversations/{id}/persona` | `persona:read` | 当前人格版本 |
| `PUT /api/v1/conversations/{id}/persona` | `policy:write` | 写入新人格版本：`{"persona":{…},"reason":"…"}` 或 `{"rollback_to":n,"reason":"…"}` |
| `GET /api/v1/conversations/{id}/policy` | `admin:read` | 当前群策略版本 |
| `PUT /api/v1/conversations/{id}/policy` | `policy:write` | 写入新群策略版本：`{"policy":{…},"reason":"…"}` |
| `GET /healthz` | 无 | 健康检查 |
| `GET /metrics` | 无 | Prometheus 指标（`server.metrics_enabled: false` 可关闭） |

会话 `{id}` 为标准会话 ID，如 `group_<群号>`、`private_<QQ号>`。同一幂等键重放返回首次响应并带 `Idempotent-Replayed: true`，同键不同内容返回 409。错误响应统一为：

```json
{"error":{"code":"FORBIDDEN","message":"…","retryable":false,"retry_after_ms":0,"trace_id":"trace_…"}}
```

请求与响应的 Go 类型见 [`pkg/conloomapi`](pkg/conloomapi)，Go SDK 见 [`pkg/client`](pkg/client)。

## 部署为服务

`/healthz` 与 `/metrics` 不需要鉴权。若要让别的机器上的 Adapter 访问核心而把 `server.listen` 改为非本地地址，请用防火墙限制来源，或关闭 `metrics_enabled`。

### Linux（systemd）

仓库的 [`deploy/systemd/`](deploy/systemd) 提供两个单元：`conloom.service`（核心）与 `conloom-napcat.service`（Adapter，依赖核心），都以 `conloom` 用户运行、崩溃 3 秒后自动重启。

```sh
sudo useradd --system --home-dir /opt/conloom --shell /usr/sbin/nologin conloom
sudo mkdir -p /opt/conloom
sudo cp conloom conloom.yaml .env /opt/conloom/
sudo chown -R conloom:conloom /opt/conloom
sudo chmod 600 /opt/conloom/.env
sudo cp deploy/systemd/conloom.service deploy/systemd/conloom-napcat.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now conloom conloom-napcat
journalctl -u conloom -u conloom-napcat -f
```

### macOS（launchd）

保存为 `~/Library/LaunchAgents/io.github.kinako404.conloom.plist`（路径改成你的实际目录）：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.github.kinako404.conloom</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/<你的用户名>/conloom/conloom</string>
    <string>serve</string>
    <string>-c</string>
    <string>/Users/<你的用户名>/conloom/conloom.yaml</string>
  </array>
  <key>WorkingDirectory</key><string>/Users/<你的用户名>/conloom</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/Users/<你的用户名>/conloom/conloom.log</string>
</dict>
</plist>
```

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.kinako404.conloom.plist
```

Adapter 同理：复制一份，`Label` 改为 `io.github.kinako404.conloom-napcat`，参数改为 `adapter napcat -c …`，日志文件另起名字。

### Windows（NSSM）

`conloom.exe` 是普通控制台程序，不实现 Windows 服务控制协议，不能直接用 `sc.exe create` 注册（启动会报 1053）。推荐用 [NSSM](https://nssm.cc/) 包装：

```bat
nssm install ConLoom C:\conloom\conloom.exe serve -c C:\conloom\conloom.yaml
nssm set ConLoom AppDirectory C:\conloom
nssm set ConLoom AppStderr C:\conloom\conloom.log
nssm start ConLoom

nssm install ConLoomNapCat C:\conloom\conloom.exe adapter napcat -c C:\conloom\conloom.yaml
nssm set ConLoomNapCat AppDirectory C:\conloom
nssm set ConLoomNapCat AppStderr C:\conloom\conloom-napcat.log
nssm set ConLoomNapCat DependOnService ConLoom
nssm start ConLoomNapCat
```

## 数据与迁移

全部状态在一个数据目录里，全部配置在 `conloom.yaml` 加可选的 `.env`，二进制没有其他依赖：

```text
<data_dir>/
  conloom.db         SQLite 数据库（运行时旁边还有 conloom.db-wal、conloom.db-shm）
  napcat-dedup.log   NapCat Adapter 的已发送记录（按 action_id 去重，保留 24 小时；位置由 adapters.napcat.state_dir 决定）
```

迁移到另一台机器：

1. 在旧机器上导出一致快照（服务运行中也可以执行）：

   ```sh
   ./conloom backup -c conloom.yaml -o conloom-backup.db
   ```

2. 在新机器下载对应平台的二进制，拷贝 `conloom.yaml`、`.env`、备份文件，以及 `napcat-dedup.log`（若 Adapter 也一起迁移）。
3. 把备份文件放到新机器的 `<data_dir>/conloom.db`（确认该目录下没有遗留的 `conloom.db-wal`、`conloom.db-shm`）。
4. 停止旧实例（先停 Adapter 再停核心），启动新实例（先启核心再启 Adapter）。

备份之后到停止旧实例之前收到的消息不会出现在备份里；若要一条不丢，可以先停止旧实例再执行 `conloom backup`（服务停止时同样可用）。核心与 Adapter 可以分别迁移：Adapter 只需能访问 NapCat 与核心 API 地址。数据库的恢复方法始终是把备份文件放到 `<data_dir>/conloom.db`。

## 基准

在 Intel Xeon E5-2680 v4 @ 2.40GHz（56 线程，linux/amd64）上运行 `go test -run '^$' -bench . -benchtime 2s ./internal/app/` 的原始结果：

```text
goos: linux
goarch: amd64
pkg: github.com/kinako404/conloom/internal/app
cpu: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
BenchmarkIngestObserve-56    	    3386	    730560 ns/op	   28351 B/op	     393 allocs/op
BenchmarkMentionToAck-56     	     439	   5386188 ns/op	  316897 B/op	    4327 allocs/op
PASS
ok  	github.com/kinako404/conloom/internal/app	6.692s
```

两项都是经过真实 Gateway、SQLite（WAL，磁盘临时目录）与 Go 客户端的完整 HTTP 往返。`BenchmarkIngestObserve` 并行提交只落库不回复的群消息，约 0.73 ms/条（所有写入由 SQLite 单写连接串行化，约 1.4k 条/秒）；`BenchmarkMentionToAck` 顺序测量一条 @Bot 消息从提交到回复被 Outbox 拉取并 ACK 的全过程，约 5.4 ms，其中模型是立即返回的假服务、合并窗口为 0。真实环境中的回复延迟主要取决于模型本身（通常为秒级）。

## 隐私

- 日志默认**不记录**消息正文，只记 ID、长度、会话、路由与耗时；本地调试时可设 `observability.log_message_content: true`。
- 只有 `enabled_conversations` 中的会话（以及按 `private_chat` 开放的私聊）的消息会被保存；其他会话的消息既不转发也不落库。
- 消息保存在本机 `<data_dir>/conloom.db` 中，用作对话上下文；请像对待聊天记录一样保护数据目录和备份文件。
- 组装好的上下文会发送到你配置的模型 endpoint；`privacy.data_location: remote` 表示数据会离开本机。
- 模型输出会剥离平台控制码，并对密钥形态的字符串与配置中的任何密钥打码后才发送。

## 开发

```sh
go vet ./...
CGO_ENABLED=1 go test -race ./...                        # -race 需要 cgo 与 C 编译器
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build ./cmd/conloom   # 发布构建一律不用 cgo
```

推送 `v*` 标签时，GitHub Actions 用 GoReleaser 构建六个平台的压缩包与 `checksums.txt` 并发布到 Releases。

## 许可证

[MIT](LICENSE)
