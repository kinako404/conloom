# 接入 Telegram

`conloom adapter telegram` 把一个 Telegram 机器人接到 ConLoom。它用 Bot API 的长轮询（`getUpdates`）收消息，用 `sendMessage` 回复，只主动连出去，不需要公网地址、域名或证书。

它处理私聊、群（group）和超级群（supergroup）；频道（channel）的消息跳过，记一条 debug 日志。机器人在哪些群里工作、和哪些人私聊，由核心按配置决定，适配器把收到的消息都交给核心。

## 1. 在 BotFather 创建机器人

1. 在 Telegram 里打开 [@BotFather](https://t.me/BotFather)，发送 `/newbot`，按提示填写显示名和用户名（用户名必须以 `bot` 结尾）。
2. BotFather 会回复一个 token，形如 `<数字>:<一串字母数字>`。**这就是机器人的全部凭据**，拿到它的人可以冒充你的机器人。只把它写进 `.env`，不要发给别人、贴进聊天记录或提交到仓库。
3. token 冒号前面的数字是机器人的数字 ID，下面配置 `platform_self_id` 时要用。
4. 要在群里用，再决定隐私模式（见 [第 4 节](#4-群里的隐私模式)）；不打算进群的话，可以发送 `/setjoingroups` 选 `Disable`，防止别人把它拉进群。

## 2. 配置

`conloom.example.yaml` 默认只接 QQ，Telegram 用到的三段里有两段是注释掉的，用 `# ---- Telegram 示例开始` 和 `# ---- Telegram 示例结束` 标出：

1. **取消注释** `api_tokens` 里的 `telegram` 令牌，和 `bots` 里的 `bot_tg` 整段：把两个标记之间每行开头的 `# ` 删掉，缩进保持不变。标记行本身是注释，留着或删掉都可以。
2. `adapters.telegram` 段本来就没有注释，不用改。它只在运行 `conloom adapter telegram` 时才校验，所以只接 QQ 时它的变量留空也不影响 `serve`。
3. 在 `.env`（`chmod 600 .env`）里设置这些变量：

```sh
CONLOOM_TG_BOT_TOKEN=<BotFather 给的 token>
CONLOOM_TG_ADAPTER_TOKEN=<随机长字符串，适配器调用核心 API 用>
CONLOOM_TG_BOT_ID=<token 冒号前的数字>
CONLOOM_TG_ADMIN_ID=<你自己的 Telegram 用户 ID>
CONLOOM_TG_GROUP_ID=<要开启的群的 ID，负数，如 -100 开头>
CONLOOM_TG_FRIEND_ID=<允许私聊的另一个人的用户 ID>
```

暂时不开群或不放行别人私聊时，删掉 `bot_tg` 里对应的那一行，也就不需要设置对应的变量（取消注释之后，被引用的变量不能为空，否则启动时报错）。

取消注释之后，这三段是这样的：

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
    enabled_conversations:
      - {type: group, platform_id: "${CONLOOM_TG_GROUP_ID}"}     # 开启的群
      - {type: private, platform_id: "${CONLOOM_TG_FRIEND_ID}"}  # 允许私聊的人
    private_chat: allowlist

adapters:
  telegram:
    token: ${CONLOOM_TG_BOT_TOKEN}
    core_url: http://127.0.0.1:8787
    core_token: ${CONLOOM_TG_ADAPTER_TOKEN}
    bot_instance_id: bot_tg
```

- 适配器令牌需要 `tasks:read`：适配器用它查回复任务是否已经结束，以便及时停止“正在输入”。缺少这个权限时适配器会打一条警告，改为在回复发出或 60 秒后停止。
- 各字段的默认值和取值范围见 [配置参考](configuration.md#adapters)。

**谁可以私聊**（`private_chat`）：

| 取值 | 谁的私聊会被回复 |
|---|---|
| `allowlist`（示例配置） | `admins` 里的人，以及 `enabled_conversations` 里以 `{type: private, platform_id: "<用户 ID>"}` 列出的人 |
| `admins` | 只有 `admins` 里的人 |
| `all` | 所有人 |
| `off` | 没有人 |

不被允许的私聊不回复，防止陌生人消耗模型额度。名单写在配置里，增删人要重启核心；`private` 项只在 `allowlist` 下生效。比如再放行两个人：

```yaml
    enabled_conversations:
      - {type: private, platform_id: "20002"}
      - {type: private, platform_id: "20003"}
    private_chat: allowlist
```

**不知道自己的用户 ID？** 先把 `CONLOOM_TG_ADMIN_ID` 临时设成任意数字（比如 `0`，这一项不能为空）启动核心和适配器，用你的账号给机器人发一句话。核心的日志里会有一行 `event accepted`，其中 `conversation=bot_tg:telegram:private:private_<数字>`，这个数字就是你的用户 ID；同一行的 `suppressed_reason=private_not_allowed` 说明这条消息因为你还不在名单里而被忽略。别人的用户 ID 也可以这样拿到。填好后重启核心即可。

## 3. 开启群聊

1. 把机器人拉进群。如果要它在群里看到所有消息，先按 [第 4 节](#4-群里的隐私模式) 处理隐私模式。
2. 在群里发一句话。群还没开启时，核心会忽略它，适配器的日志里会出现一行 info 日志（每个群只打一次），消息为 `the bot is in a Telegram group the core does not serve; list it in bots[].enabled_conversations to enable it`，带两个字段，例如 `chat_id=-1001234567890`、`conversation_id=group_-1001234567890`。

   `chat_id` 就是群 ID。核心的日志里同样有 `event accepted … conversation=bot_tg:telegram:group:group_<群 ID> suppressed_reason=not_enabled`。
3. 把它写进 `enabled_conversations`：`{type: group, platform_id: "<群 ID>"}`。Telegram 的群 ID 是负数（超级群以 `-100` 开头），**要加引号**；用 `${CONLOOM_TG_GROUP_ID}` 时引号已经在示例里了。
4. 重启核心。

开启后，群里的行为和 QQ 群一样：@ 机器人或回复机器人的消息时它会回应，其他消息只记录（作为以后的上下文），话题合适时可能主动加入，详见 [使用指南](usage.md)。核心的标准会话 ID 是 `group_<群 ID>`，负号原样保留，通过 API 访问时也这样写，例如 `/api/v1/conversations/group_-1001234567890/policy`。

普通群升级成超级群时群 ID 会变（变成 `-100…` 开头的新 ID），要按新 ID 重新开启。

## 4. 群里的隐私模式

Telegram 的机器人默认处于隐私模式。按 [Telegram 的说明](https://core.telegram.org/bots/features#privacy-mode)，这时它在群里只收到：

- 明确发给它的命令（如 `/help@机器人用户名`），以及它是最后一个在群里发言的机器人时的普通命令；
- 回复它的消息的消息；
- 通过它发送的 inline 消息、服务消息。

只 @ 机器人、不回复也不是命令的消息，不在官方列出的范围内。也就是说，隐私模式下机器人看不到群里的普通聊天：没有上下文，在聊会话和主动加入都无法工作，@ 它也不一定收得到。

**关闭隐私模式**：在 @BotFather 发送 `/setprivacy`，选中机器人，选 `Disable`。之后机器人像普通成员一样收到群里所有消息。**这个设置只对之后加入的群生效**，已经在群里的要先移出、再拉回来。另一个办法是把机器人设为群管理员：管理员机器人总能收到所有消息，不受隐私模式影响。

取舍：

- 关闭后，开启的群里所有消息都会交给核心并存进数据库（作为上下文；开启了记忆的群还会整理成摘要和笔记）。请让群成员知道群里有一个会记录聊天的机器人。
- 没开启的群，核心只留下幂等记录，不保存消息正文；但消息仍会经过适配器和核心，所以不要把关闭了隐私模式的机器人放进不打算开启的群。
- 保持隐私模式时，机器人只能回应发给它的命令和回复它的消息，适合只想“有问才答”的群。

## 5. 运行

核心先启动（`conloom serve -c conloom.yaml`），然后：

```sh
conloom adapter telegram -c conloom.yaml
```

适配器启动时依次检查 token（`getMe`）、删除可能设置过的 webhook（`deleteWebhook`，保留尚未拉取的消息）、检查核心令牌（`/api/v1/capabilities`）。token 被 Telegram 拒绝、或者 `core_token` 绑定的不是 `bot_instance_id` 指定的机器人实例时，适配器直接报错退出；网络或核心暂时不可用则退避重试（1 秒起，最长 30 秒）。

作为 systemd 服务运行时，使用仓库里的 `deploy/systemd/conloom-telegram.service`，做法与 NapCat 适配器相同，见 [部署与运维](deployment.md#linuxsystemd)：

```sh
sudo cp deploy/systemd/conloom-telegram.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now conloom-telegram
journalctl -u conloom-telegram -f
```

一个机器人只能运行一个适配器进程：Telegram 只允许一个 `getUpdates` 连接，第二个会收到 409 Conflict。运行中遇到 409，适配器打一条 error 日志（“another process may be polling this bot”）并退避重试。

**需要代理才能访问 Telegram 时**：适配器遵循标准的 `HTTPS_PROXY` / `NO_PROXY` 环境变量（访问本机核心不走代理）。注意 `.env` 只用于展开配置里的 `${VAR}`，不会设置进程环境变量；systemd 下用 `sudo systemctl edit conloom-telegram` 加上 `[Service]` 段的 `Environment=HTTPS_PROXY=http://<代理地址>:<端口>`。也可以把 `api_base` 指向自建的 Bot API 服务器或反向代理。

## 6. 行为

**收消息**

- 每条更新先提交给核心，核心接受之后才在下一次 `getUpdates` 里确认它（offset 移到它之后）。核心不可用时适配器退避重试同一条，后面的更新留在 Telegram 那边；Telegram 保存未确认的更新 24 小时，所以核心或适配器停机期间的消息不会丢。重启后同一条更新会再来一次，核心按幂等键（`telegram:<机器人ID>:<update_id>`）去重，不会回复两遍。停机超过 `runtime.stale_task_after`（默认 2 分钟）的消息只记录、不回复。
- 核心明确拒绝的更新（4xx，比如请求不合法）记一条错误日志后跳过。
- 文字原样提交；图片、贴纸、语音和音频、视频、文件分别写成 `[图片]`、`[表情]`、`[语音]`、`[视频]`、`[文件]`，带说明文字时接在后面。图片本身不会下载。位置、联系人、投票等其他类型跳过。
- `/start`（含带参数的深层链接）转成普通文本“你好”，由人设自己打招呼；群里的 `/start@<机器人用户名>` 同时算 @ 机器人。
- 引用回复会被保留：引用机器人的某条回复时，核心能认出这是在回复机器人，并把被引用的内容放进上下文。

**群里的 @ 和命令**

- `@<机器人用户名>` 算 @ 机器人，这段文字从正文里去掉（核心另有标注）；@ 其他人保留在正文里，同时作为“@ 了某人”交给核心。没有用户名的人被 @ 时（Telegram 的 text_mention），按用户 ID 对应到群友。
- 从命令菜单点出的 `/pause@<机器人用户名>` 会去掉后缀，变成 `/pause`。后缀是别的机器人的命令（如 `/help@other_bot`）不是发给这个机器人的，原样作为普通消息交给核心，不会被执行。

**发消息**

- 回复以纯文本发送，不设置 `parse_mode`，模型输出里的 Markdown 或 HTML 不会被解释。群里是否引用原消息由群策略的 `reply.quote` 决定。
- 超过 4096 字（按 Telegram 的 UTF-16 计数）时拆成多条，优先在空行、其次在换行处断开；只有第一条带引用。
- 收到 429 限流时，按返回的 `retry_after` 等待后重试，最多 5 次。
- 核心判定要回复后，适配器每 4 秒发送一次“正在输入”。每次发送前先向核心查一次这个回复任务的状态，任务已结束（成功、失败或取消，包括模型决定不回复的情况）就停止；回复发出时也停止；无论如何最长 60 秒。

**不会重发**

适配器在 `adapters.telegram.state_dir`（默认 `storage.data_dir`）下的 `telegram-dedup.log` 里记录已发送的动作，规则与 NapCat 适配器相同：已发送的只补确认；Telegram 明确拒绝（例如用户屏蔽了机器人、机器人被移出群），或者请求发出后没拿到明确结果（超时、5xx），都以失败确认、以后不再重发，宁可少发一条也不发两遍。拆成多条的长回复，第一条发出后如果后面某条失败，剩下的部分会被放弃。

**token 不会出现在日志里**

Bot API 的地址本身包含 token。适配器的所有日志和错误信息里，token 都被替换成 `REDACTED`。

**已知限制**

- 开启了话题（Topics）的论坛型超级群：回复不带话题 ID，会发到“General”话题；话题里的消息在 Telegram 里会带一个指向话题首条消息的引用，核心找不到那条消息，会把它当成“回复了一条更早的消息”，这会影响在聊会话和主动加入的判断。
- 群里匿名管理员和以频道身份发言的消息（发送者是机器人账号）会被跳过。

## 7. 重新生成 token

token 泄露了，或者只是想定期更换：

1. 在 @BotFather 发送 `/revoke`，选中这个机器人。旧 token 立即失效，BotFather 回复新的 token。
2. 把新 token 写进 `.env` 的 `CONLOOM_TG_BOT_TOKEN`。
3. 重启适配器：`sudo systemctl restart conloom-telegram`。核心不用重启。

旧 token 失效后、适配器重启前，正在运行的适配器会在日志里报 401 Unauthorized 并不断重试；用旧 token 启动的适配器会报“the bot token was rejected”后退出。机器人的数字 ID 不会因此改变，`platform_self_id` 不用改。只想查看当前 token 时，在 BotFather 发送 `/token`。

## 8. 常见问题

| 现象 | 原因与处理 |
|---|---|
| 日志里 `getUpdates conflict` 带 `error 409: Conflict: terminated by other getUpdates request` | 另一个进程在用同一个 token 拉取更新。停掉另一个进程（另一台机器上的适配器、调试脚本等） |
| 日志里 `error 409` 提到 webhook | 适配器运行期间有人又给机器人设置了 webhook。适配器只在启动时删除 webhook，重启适配器即可 |
| 私聊没反应，核心日志 `suppressed_reason=private_not_allowed` | 发送者不在名单里：`allowlist` 下把他加进 `enabled_conversations` 的 `private` 项，或加进 `admins` |
| 群里没反应，日志提示群没开启（`not_enabled`） | 按第 3 节把群 ID 加进 `enabled_conversations` 并重启核心 |
| 群里只有 @ 它的时候有时没反应、也不接话 | 隐私模式还开着，机器人看不到这些消息。按第 4 节关闭隐私模式并重新拉它进群，或设为群管理员 |
| 日志警告 `lacks the tasks:read scope` | 适配器令牌缺少 `tasks:read`，“正在输入”会持续到回复发出或 60 秒。给令牌加上这个 scope |
| 启动即退出，`core_token belongs to bot instance …` | `adapters.telegram.core_token` 用了别的机器人实例的令牌，换成绑定 `bot_tg` 的那个 |
| 回复里出现 `[图片]` 之类的字样 | 这是用户发了图片等媒体，适配器只告诉模型“这里有一张图片”，不会下载内容 |
