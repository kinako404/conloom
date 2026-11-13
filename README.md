# ConLoom 织语

让对话沉淀为记忆，让记忆塑造人格。

ConLoom 是一个用 Go 编写的群聊对话智能体框架。它让一个由大模型驱动的机器人像群友一样参与群聊：被叫到时认真回应，大家聊得热闹时不乱插话，话题合适时自然地加入一两句。

它适合想在自己的群里放一个“有分寸”的聊天机器人的个人和小团队：平台适配器把聊天消息交给核心，核心决定什么时候说话、说什么，再可靠地把回复送回平台。目前接入的平台是 QQ（通过 NapCat）和 Telegram（通过 Bot API），模型可以是任意 OpenAI 兼容接口。

## 特性

- **像群友一样参与群聊**：不乱插话，不句句都回；正在聊天时按“轮”判断值不值得接话，对方转头和别人聊时识趣退出；没在聊时，只在群里自然停顿、话题相关时才主动加入一两句。
- **主动搭话由每个人自己决定**：只有 @ 过机器人的群友才可能被主动搭话，每个人用 `/主动 关|低|中|高` 调自己的档位，管理员另有群总开关。
- **可配置的人设模板**：风格、行为偏好，以及角色名、角色设定、说话方式和示例对话；按群应用，每次修改都有版本，可以回滚。内置的核心规则始终优先，人设改变不了它们。
- **QQ 和 Telegram**：QQ 群聊和私聊通过 NapCat 接入；Telegram 私聊和群聊通过 Bot API 长轮询接入，不需要公网地址，见 [接入 Telegram](docs/telegram.md)。适配器只用公开 API 和核心交互，核心不认识任何平台。
- **可靠投递**：事件幂等、发件箱持久化、至少一次投递加适配器端去重；重启后恢复未完成的任务，过时的回复不再补发。
- **技能（工具调用）**：给机器人启用 `memory`、`time` 等技能后，模型在私聊和直接对话里可以调用内置工具（记住一件事、查聊天记录、看时间），还能在 `skills.dir` 里新增或改写技能说明，见 [技能](docs/skills.md)。
- **OpenAI 兼容的模型路由**：按短上下文、长上下文两个角色选择模型，换模型只改配置；带熔断和失败重试。
- **单二进制、跨平台**：不需要 cgo、数据库服务或 Docker，Linux、macOS、Windows 的 amd64 和 arm64 开箱即用，数据都在一个 SQLite 文件里。
- **可观测**：结构化日志、trace_id、Prometheus 指标；日志默认不记录消息正文。

## 架构

```text
QQ 群 ⇄ NapCat ⇄(OneBot 11 正向 WS)⇄ conloom adapter napcat   ⇄(REST /api/v1)⇄ conloom serve ⇄ 模型（OpenAI 兼容）
Telegram 私聊/群 ⇄(Bot API 长轮询)⇄  conloom adapter telegram ⇄(REST /api/v1)⇄       │
                                                                                <data_dir>/conloom.db（SQLite）
```

适配器和核心是同一个二进制的两个进程，只通过公开的 REST API 交互。消息的完整往返、调度、群聊参与规则和可靠性语义见 [架构文档](docs/architecture.md)。

## 快速开始

**1. 获取二进制**。在 [Releases](https://github.com/kinako404/conloom/releases) 下载对应平台的压缩包并解压；或者从源码安装（需要 Go 1.26 或更高版本）：

```sh
go install github.com/kinako404/conloom/cmd/conloom@latest
```

**2. 准备配置**。复制示例配置，并在同一目录新建 `.env`（`chmod 600 .env`）填入真实值，示例配置里的 `${VAR}` 会从这里展开：

```sh
cp conloom.example.yaml conloom.yaml
```

```sh
CONLOOM_ADAPTER_TOKEN=<随机长字符串>
CONLOOM_ADMIN_TOKEN=<另一个随机长字符串>
CONLOOM_BOT_SELF_ID=<机器人的 QQ 号>
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

这样配好的是 QQ。接 Telegram 见 [接入 Telegram](docs/telegram.md)：要在示例配置里取消注释两段，并另设几个变量。

**3. 启动核心**（默认只监听本机 8787 端口）：

```sh
./conloom serve -c conloom.yaml
curl http://127.0.0.1:8787/healthz   # 返回 ok
```

**4. 在终端里试聊**。另开一个终端，用 Mock 适配器对话；每行一条消息，群聊里以 `@` 开头表示 @ 机器人，Ctrl+D（Windows 为 Ctrl+Z 回车）退出：

```sh
./conloom mock -c conloom.yaml                                   # 以第一个管理员的身份私聊
./conloom mock -c conloom.yaml -conv group:10001 -user 20001:小明   # 模拟群聊
```

**5. 接入 QQ**。在 NapCat 里开启 OneBot 11 正向 WebSocket 服务并设置 access token，然后启动适配器，在启用的群里 @ 机器人即可：

```sh
./conloom adapter napcat -c conloom.yaml
```

每个机器人实例只能运行一个适配器进程，不要同时运行 Mock 和 NapCat 适配器。配置字段见 [配置参考](docs/configuration.md)，作为系统服务运行见 [部署与运维](docs/deployment.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [使用指南](docs/usage.md) | 命令行、聊天命令、机器人在群里怎么说话、人设模板的用法 |
| [配置参考](docs/configuration.md) | 配置文件结构、各字段和默认值、群策略、人设模板字段、环境变量 |
| [HTTP API](docs/api.md) | 端点、鉴权范围、请求与响应、错误格式 |
| [部署与运维](docs/deployment.md) | systemd / launchd / NSSM、数据目录、备份与迁移、指标、隐私 |
| [架构](docs/architecture.md) | 进程与数据流、任务调度、群聊参与的规则和上限、模型管线、可靠性 |
| [开发](docs/development.md) | 构建、测试、基准、CI 与发布 |
| [设计记录](docs/superpowers/) | 各阶段的设计文档与实施计划（另有 [验收记录](docs/p1-acceptance.md)） |

## 路线图

- **长期记忆**：按群开启的滚动摘要、群友笔记和好感度已经可用（见 [使用指南](docs/usage.md#记忆与好感度)）；后续是混合检索、向量、长期事实、`/remember` 与按条遗忘、分层遗忘，以及人格随相处逐渐演化。
- **工具能力**：技能框架已支持内置工具；后续接入 Web 搜索、ComfyUI 等外部工具。
- **存储与扩展**：PostgreSQL / pgvector / Redis 存储实现，多实例水平扩展。
- **更多能力**：更多平台适配器、多 Agent、流式输出。

## 许可证

[MIT](LICENSE)
