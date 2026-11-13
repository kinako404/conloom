# 接入 Telegram

`conloom adapter telegram` 把一个 Telegram 机器人接到 ConLoom。它用 Bot API 的长轮询（`getUpdates`）收消息，用 `sendMessage` 回复，只主动连出去，不需要公网地址、域名或证书。

目前**只处理私聊**：群、超级群和频道里的消息会被跳过（记一条 debug 日志）。

## 1. 在 BotFather 创建机器人

1. 在 Telegram 里打开 [@BotFather](https://t.me/BotFather)，发送 `/newbot`，按提示填写显示名和用户名（用户名必须以 `bot` 结尾）。
2. BotFather 会回复一个 token，形如 `<数字>:<一串字母数字>`。**这就是机器人的全部凭据**，拿到它的人可以冒充你的机器人。只把它写进 `.env`，不要发给别人、贴进聊天记录或提交到仓库。
3. token 冒号前面的数字是机器人的数字 ID，下面配置 `platform_self_id` 时要用。
4. 可选：发送 `/setjoingroups`，选中这个机器人后选 `Disable`，防止别人把它拉进群（适配器反正不处理群消息）。

## 2. 配置

在 `.env`（`chmod 600 .env`）里加上：

```sh
CONLOOM_TG_BOT_TOKEN=<BotFather 给的 token>
CONLOOM_TG_ADAPTER_TOKEN=<随机长字符串，适配器调用核心 API 用>
CONLOOM_TG_BOT_ID=<token 冒号前的数字>
CONLOOM_TG_ADMIN_ID=<你自己的 Telegram 用户 ID>
```

`conloom.example.yaml` 已经包含所需的三段，复制过去即可：

```yaml
api_tokens:
  - name: telegram
    token: ${CONLOOM_TG_ADAPTER_TOKEN}
    bot_instance_id: bot_tg
    scopes: [events:write, tasks:read, outbox:read, outbox:ack, persona:read]

bots:
  - id: bot_tg
    platform: telegram
    platform_self_id: ${CONLOOM_TG_BOT_ID}
    admins: ["${CONLOOM_TG_ADMIN_ID}"]
    private_chat: admins     # 只有管理员能私聊；想对所有人开放改成 all

adapters:
  telegram:
    token: ${CONLOOM_TG_BOT_TOKEN}
    core_url: http://127.0.0.1:8787
    core_token: ${CONLOOM_TG_ADAPTER_TOKEN}
    bot_instance_id: bot_tg
```

- `private_chat: admins` 时，不在 `admins` 里的人发来的消息会被核心忽略，防止陌生人消耗模型额度。
- 各字段的默认值和取值范围见 [配置参考](configuration.md#adapters)。

**不知道自己的用户 ID？** 先把 `CONLOOM_TG_ADMIN_ID` 临时设成任意数字（比如 `0`，这一项不能为空）启动核心和适配器，用你的账号给机器人发一句话。核心的日志里会有一行 `event accepted`，其中 `conversation=bot_tg:telegram:private:private_<数字>`，这个数字就是你的用户 ID；同一行的 `suppressed_reason=private_not_allowed` 说明这条消息因为你还不是管理员而被忽略。把它填进 `CONLOOM_TG_ADMIN_ID` 后重启核心即可。

## 3. 运行

核心先启动（`conloom serve -c conloom.yaml`），然后：

```sh
conloom adapter telegram -c conloom.yaml
```

适配器启动时会依次检查 token（`getMe`）和核心令牌（`/api/v1/capabilities`）。token 被 Telegram 拒绝、或者 `core_token` 绑定的不是 `bot_instance_id` 指定的机器人实例时，适配器直接报错退出；网络或核心暂时不可用则退避重试（1 秒起，最长 30 秒）。

作为 systemd 服务运行时，使用仓库里的 `deploy/systemd/conloom-telegram.service`，做法与 NapCat 适配器相同，见 [部署与运维](deployment.md#linuxsystemd)：

```sh
sudo cp deploy/systemd/conloom-telegram.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now conloom-telegram
journalctl -u conloom-telegram -f
```

一个机器人只能运行一个适配器进程：Telegram 只允许一个 `getUpdates` 连接，第二个会收到 409 Conflict。

**需要代理才能访问 Telegram 时**：适配器遵循标准的 `HTTPS_PROXY` / `NO_PROXY` 环境变量（访问本机核心不走代理）。注意 `.env` 只用于展开配置里的 `${VAR}`，不会设置进程环境变量；systemd 下用 `sudo systemctl edit conloom-telegram` 加上 `[Service]` 段的 `Environment=HTTPS_PROXY=http://<代理地址>:<端口>`。也可以把 `api_base` 指向自建的 Bot API 服务器或反向代理。

## 4. 行为

**收消息**

- 每条更新先提交给核心，核心接受之后才在下一次 `getUpdates` 里确认它（offset 移到它之后）。核心不可用时适配器退避重试同一条，后面的更新留在 Telegram 那边；Telegram 保存未确认的更新 24 小时，所以核心或适配器停机期间的消息不会丢。重启后同一条更新会再来一次，核心按幂等键（`telegram:<机器人ID>:<update_id>`）去重，不会回复两遍。停机超过 `runtime.stale_task_after`（默认 2 分钟）的消息只记录、不回复。
- 核心明确拒绝的更新（4xx，比如请求不合法）记一条错误日志后跳过。
- 文字原样提交；图片、贴纸、语音和音频、视频、文件分别写成 `[图片]`、`[表情]`、`[语音]`、`[视频]`、`[文件]`，带说明文字时接在后面。图片本身不会下载。位置、联系人、投票等其他类型跳过。
- `/start`（含带参数的深层链接）转成普通文本“你好”，由人设自己打招呼。从命令菜单点出的 `/help@<机器人用户名>` 会去掉 `@` 后缀，变成 `/help`。
- 引用回复会被保留：引用机器人的某条回复时，核心能认出这是在回复机器人，并把被引用的内容放进上下文。

**发消息**

- 回复以纯文本发送，不设置 `parse_mode`，模型输出里的 Markdown 或 HTML 不会被解释。
- 超过 4096 字（按 Telegram 的 UTF-16 计数）时拆成多条，优先在空行、其次在换行处断开；只有第一条带引用。
- 收到 429 限流时，按返回的 `retry_after` 等待后重试，最多 5 次。
- 核心判定要回复后，适配器每 4 秒发送一次“正在输入”，直到这条回复发出，最长 60 秒。

**不会重发**

适配器在 `adapters.telegram.state_dir`（默认 `storage.data_dir`）下的 `telegram-dedup.log` 里记录已发送的动作，规则与 NapCat 适配器相同：已发送的只补确认；Telegram 明确拒绝（例如用户屏蔽了机器人），或者请求发出后没拿到明确结果（超时、5xx），都以失败确认、以后不再重发，宁可少发一条也不发两遍。拆成多条的长回复，第一条发出后如果后面某条失败，剩下的部分会被放弃。

**token 不会出现在日志里**

Bot API 的地址本身包含 token。适配器的所有日志和错误信息里，token 都被替换成 `REDACTED`。

## 5. 重新生成 token

token 泄露了，或者只是想定期更换：

1. 在 @BotFather 发送 `/revoke`，选中这个机器人。旧 token 立即失效，BotFather 回复新的 token。
2. 把新 token 写进 `.env` 的 `CONLOOM_TG_BOT_TOKEN`。
3. 重启适配器：`sudo systemctl restart conloom-telegram`。核心不用重启。

旧 token 失效后、适配器重启前，正在运行的适配器会在日志里报 401 Unauthorized 并不断重试；用旧 token 启动的适配器会报“the bot token was rejected”后退出。机器人的数字 ID 不会因此改变，`platform_self_id` 不用改。只想查看当前 token 时，在 BotFather 发送 `/token`。

## 6. 常见问题

| 现象 | 原因与处理 |
|---|---|
| 日志里 `getUpdates failed` 带 `error 409: Conflict` | 另一个进程在用同一个 token 拉取更新，或者这个机器人之前设置过 webhook。停掉另一个进程；如果是 webhook，用 Bot API 的 `deleteWebhook` 删除它（这个请求的地址含 token，别留在共享终端的历史里） |
| 发消息没反应，核心日志 `suppressed_reason=private_not_allowed` | 发送者不在 `admins` 里，而 `private_chat` 是 `admins` |
| 启动即退出，`core_token belongs to bot instance …` | `adapters.telegram.core_token` 用了别的机器人实例的令牌，换成绑定 `bot_tg` 的那个 |
| 回复里出现 `[图片]` 之类的字样 | 这是用户发了图片等媒体，适配器只告诉模型“这里有一张图片”，不会下载内容 |
