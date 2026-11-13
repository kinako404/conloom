# 部署与运维

本文介绍怎么安装、作为系统服务运行、管理数据和备份，以及监控和隐私方面的注意事项。进程拓扑和重启行为见 [架构](architecture.md#1-部署拓扑)。

## 安装

**预编译二进制（推荐）**：在 [Releases](https://github.com/kinako404/conloom/releases) 下载对应平台的压缩包 `conloom_<版本>_<系统>_<架构>.tar.gz`（Windows 为 `.zip`），解压即可，不需要任何运行时。可以用同页的 `checksums.txt` 校验：

```sh
sha256sum -c checksums.txt --ignore-missing
```

压缩包内含 `conloom` 二进制、`README.md`、`LICENSE`、`conloom.example.yaml`、`deploy/` 和 `docs/`。macOS 首次运行如果被拦截，可以执行 `xattr -d com.apple.quarantine ./conloom`。

**从源码安装**：需要 Go 1.26 或更高版本（由纯 Go 的 SQLite 驱动决定）。

```sh
go install github.com/kinako404/conloom/cmd/conloom@latest
```

## 进程与网络

- 生产环境运行两个长期进程：核心 `conloom serve` 和适配器 `conloom adapter napcat`。适配器不监听端口，只主动连接 NapCat 和核心，可以部署在另一台机器上。
- 核心和适配器都只能各运行一个：核心的调度、限流等状态在进程内存里，适配器的去重记录是进程本地的。
- API、`/healthz` 和 `/metrics` 共用 `server.listen` 一个端口，默认只监听本机。`/healthz` 与 `/metrics` 不需要鉴权；如果要让别的机器上的适配器访问核心而把 `server.listen` 改为非本地地址，请用防火墙限制来源，或关闭 `metrics_enabled`。适配器和核心之间是普通 HTTP，跨机器时建议走内网或加一层 TLS 代理。
- 仓库不负责部署 NapCat，只要求 NapCat 开启 OneBot 11 正向 WebSocket 服务并设置 access token。适配器断线后自动重连（退避从 1 秒起，最长 30 秒）。

## Linux（systemd）

仓库的 [`deploy/systemd/`](../deploy/systemd) 提供两个单元：`conloom.service`（核心）和 `conloom-napcat.service`（适配器，依赖核心）。两者都以 `conloom` 用户运行、崩溃 3 秒后自动重启，停止时最多等待 90 秒（适配器会等正在发送的消息拿到结果再退出）。单元启用了 `ProtectSystem=strict`，只有 `/opt/conloom/data` 可写：该目录必须在启动前存在；如果把 `storage.data_dir` 或 `adapters.napcat.state_dir` 改到别处，要同步修改单元里的 `ReadWritePaths`。

```sh
sudo useradd --system --home-dir /opt/conloom --shell /usr/sbin/nologin conloom
sudo mkdir -p /opt/conloom/data
sudo cp conloom conloom.yaml .env /opt/conloom/
sudo chown -R conloom:conloom /opt/conloom
sudo chmod 600 /opt/conloom/.env
sudo chmod 700 /opt/conloom/data
sudo cp deploy/systemd/conloom.service deploy/systemd/conloom-napcat.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now conloom conloom-napcat
journalctl -u conloom -u conloom-napcat -f
```

`chmod 700` 那一步让数据目录只有服务用户能访问，原因见 [数据目录与权限](#数据目录与权限)。

## macOS（launchd）

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

适配器同理：复制一份，`Label` 改为 `io.github.kinako404.conloom-napcat`，参数改为 `adapter napcat -c …`，日志文件另起名字。

## Windows（NSSM）

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

## 数据目录与权限

全部状态在一个数据目录里，全部配置在 `conloom.yaml` 加可选的 `.env`，二进制没有其他依赖：

```text
<data_dir>/
  conloom.db         SQLite 数据库（运行时旁边还有 conloom.db-wal、conloom.db-shm）
  napcat-dedup.log   NapCat 适配器的已发送记录（按 action_id 去重，保留 24 小时）
```

- `napcat-dedup.log` 写在 `adapters.napcat.state_dir` 下，默认与 `storage.data_dir` 相同；适配器部署在另一台机器时，它在适配器那台机器上。
- 核心第一次创建数据目录时权限为 0700，但目录已经存在时不会修改它。数据库文件按进程的 umask 创建（常见的 umask 022 下是 0644），所以请自己把数据目录设为 0700，或在服务单元里设置 `UMask=0077`。去重记录文件为 0600。
- 数据库里的消息、任务、发件箱记录目前没有自动清理，会随使用增长；事件幂等记录保留 `runtime.idempotency_retention`（默认 7 天），主动搭话记录保留 7 天。

## 备份、迁移与升级

**备份**：服务运行中也可以导出一致快照，备份文件权限为 0600，目标文件必须不存在：

```sh
./conloom backup -c conloom.yaml -o conloom-backup.db
```

`conloom backup` 会以当前二进制的版本打开数据库，必要时先执行表结构迁移。所以不要用更新版本的二进制去备份一个仍由旧版本运行的库。数据库的恢复方法始终是把备份文件放到 `<data_dir>/conloom.db`（确认该目录下没有遗留的 `conloom.db-wal`、`conloom.db-shm`）。

**迁移到另一台机器**：

1. 在旧机器上导出快照（见上文）。
2. 在新机器下载对应平台的二进制，拷贝 `conloom.yaml`、`.env`、备份文件，以及 `napcat-dedup.log`（如果适配器也一起迁移）。
3. 把备份文件放到新机器的 `<data_dir>/conloom.db`，`napcat-dedup.log` 放到新机器的 `adapters.napcat.state_dir`（默认即 `<data_dir>`）。按上文 systemd 布局部署时，恢复的文件要交给服务用户：

   ```sh
   sudo chown conloom:conloom /opt/conloom/data/conloom.db /opt/conloom/data/napcat-dedup.log
   sudo chmod 600 /opt/conloom/data/conloom.db /opt/conloom/data/napcat-dedup.log
   ```

4. 停止旧实例（先停适配器再停核心），启动新实例（先启核心再启适配器）。

备份之后到停止旧实例之前收到的消息不会出现在备份里；如果要一条不丢，可以先停止旧实例再执行 `conloom backup`（服务停止时同样可用）。核心和适配器可以分别迁移：适配器只需要能访问 NapCat 和核心的 API 地址。

**升级**：先备份，再替换二进制并重启（先核心后适配器）。核心启动时自动按顺序应用新的表结构迁移，迁移只能前进、没有回滚脚本，所以回退到旧版本时要用升级前的备份。升级前请对照 [配置参考](configuration.md#群策略) 检查已移除的配置字段（例如 `proactive.contact_cooldown`），配置文件里残留未知字段会导致启动失败。

**回退到没有记忆层的旧版本**：记忆层的迁移（`0005_memory.sql`）只新增三张表，旧二进制可以直接在升级后的库上运行，会忽略这些表；要完全回到升级前的状态，仍然用升级前的备份。回退时注意：

- 不要在 `conloom.yaml` 里写 `memory` 段（包括 `default_group_policy.memory`），否则旧二进制会因为未知字段启动失败。回退到没有私聊记忆的版本时同理，要去掉 `default_private_policy` 段和 `memory.consolidate_min`。
- 旧二进制运行期间写入的新策略版本（群策略 API、`/pause`、`/resume`、`/主动 全群` 等）会丢掉 `memory` 段。再升级回来以后，这些群的记忆会变成关闭，需要重新开启；已有的摘要、笔记和好感度不会丢失，重新开启后按“关闭与重新开启”的规则继续整理（积压超过 `memory.backlog` 条时只整理最近的这么多条）。

## 监控

- **健康检查**：`GET /healthz` 查一次数据库，正常返回 `ok`，失败返回 503。
- **日志**：`log/slog` 结构化日志写到 stderr（systemd 下进 journald），格式和级别见 [配置参考](configuration.md#observability)。
- **指标**：`GET /metrics` 提供 Prometheus 指标，全部以 `conloom_` 开头，包括事件和任务计数、模型延迟和 token 用量、准入排队、发件箱投递、审查结果、熔断状态，以及群聊参与相关的两项：

| 指标 | 标签 | 说明 |
|---|---|---|
| `conloom_continuation_exits_total` | `reason`：`rule`、`model`、`limit`、`idle` | 在聊模式的退出次数：`rule` 为参与者规则退出，`model` 为连续跳过使会话结束，`limit` 为未点名回复达到上限使会话结束，`idle` 为超过保持时间无活动而结束 |
| `conloom_proactive_total` | `result`：`evaluated`、`sent`、`skipped` | 加入群聊：`evaluated` 为通过检查、交给模型评估，`sent` 为实际发出，`skipped` 为模型放弃，或调用模型前 / 发出前重新检查时已不允许发出 |

完整的指标列表、日志里的原因代码（如 `join skipped` 的 `few_messages`、`cooldown`、`no_eligible`）见 [架构 8 节](architecture.md#8-可观测性)。停顿检查没有加入时的日志带窗口消息数和目标资格人数，不记录兴趣词、消息正文和群友昵称。指标在进程重启后归零。

## 隐私

- 日志默认**不记录**消息正文，只记 ID、长度、会话、路由和耗时；本地调试时可以设 `observability.log_message_content: true`。
- 只有 `enabled_conversations` 中的会话（以及按 `private_chat` 开放的私聊）的消息会被保存。其他会话的消息不转发、正文不落库，只留一条不含正文的幂等记录（幂等键、请求哈希和响应），按 `idempotency_retention` 清理。
- 消息保存在本机 `<data_dir>/conloom.db` 中，用作对话上下文；被处理的事件原文（含正文）在幂等记录里保留 `idempotency_retention`。请像对待聊天记录一样保护数据目录和备份文件。
- 主动搭话的兴趣画像（每位 @ 过机器人的群友最多 50 个关键词及权重）、档位和最近一次 @ 机器人的时间保存在 `conloom.db` 中，不写入日志，也不会出现在群消息里；加入群聊时只把在场目标资格人合并后的前 5 个兴趣词放进发给模型的提示。在聊会话和停顿计时器只在内存里，不落库。
- 组装好的上下文会发送到你配置的模型 endpoint，也就是说消息内容会离开本机（除非模型部署在本机）。
- 模型输出会剥离平台控制码，并对密钥形态的字符串和配置中的任何密钥打码后才发送。
