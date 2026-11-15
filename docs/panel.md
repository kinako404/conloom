# Web 管理面板

面板是核心自带的网页管理界面，给部署者本人用：看模型用量和运行状态，浏览和修改各个会话的策略、人设和记忆，新建和管理只在面板里聊天的 bot，查看任务、审计记录和当前配置。页面和脚本用 `go:embed` 打包进 `conloom` 二进制，不需要另装任何东西，也不引用外部资源。设计见 `docs/superpowers/specs/2026-11-13-web-panel-design.md`。

## 启用

在 `conloom.yaml` 里加上 `panel` 段，重启核心即可；整段省略（或只写一个空的 `panel:`）时不启用面板，`/panel/` 下的路径都不存在。

```yaml
panel:
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
  session_ttl: 168h      # 登录后最长有效期
  idle_timeout: 12h      # 多久不操作就要重新登录
  cookie_secure: false   # 经 HTTPS 反向代理访问时改为 true
  # listen: 0.0.0.0:8788 # 面板单独的端口，见下文“用单独端口直接访问”
```

- 密码哈希用 `conloom panel hash-password` 生成，写进 `.env`，步骤见 [部署：Web 管理面板](deployment.md#web-管理面板)。字段说明见 [配置参考：panel](configuration.md#panel)。
- 默认面板和 API 共用 `server.listen` 这一个端口：`/panel/` 是页面，`/panel/api/` 是页面用的 JSON 接口，`/panel` 会跳转到 `/panel/`。设了 `panel.listen` 时面板改到那个单独的端口，见 [用单独端口直接访问](#用单独端口直接访问)。
- 面板用自己的登录会话（cookie）鉴权，和 `api_tokens` 的 Bearer 令牌无关。现有的 `/api/v1/*`、`/healthz`、`/metrics` 行为不变。
- 可以配置多个账号，权限都一样，都是管理员。

## 访问

核心默认只监听本机（`127.0.0.1:8787`）。从自己的电脑访问服务器上的面板，最简单的是 SSH 隧道：

```sh
ssh -L 8787:127.0.0.1:8787 <用户>@<服务器>
```

保持这个连接，在本机浏览器打开 `http://127.0.0.1:8787/panel/`。本机 8787 端口被占用时换一个本地端口，例如 `-L 18787:127.0.0.1:8787`，然后打开 `http://127.0.0.1:18787/panel/`。

需要不经 SSH 直接从公网访问时，推荐用反向代理负责 HTTPS，并把 `panel.cookie_secure` 改为 `true`，示例见 [部署：经 HTTPS 反向代理访问](deployment.md#经-https-反向代理访问)。不要把 `server.listen` 直接改成公网地址：那样连 `/metrics`（不需要鉴权）和 API 一起暴露出去，面板也没有 HTTPS。只想开放面板时用下面的 `panel.listen`。

### 用单独端口直接访问

`panel.listen` 让核心多开一个只提供面板的端口，`server.listen` 保持只监听本机：

```yaml
panel:
  listen: 0.0.0.0:8788
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
```

重启后在浏览器打开 `http://<服务器地址>:8788/panel/`（直接打开 `http://<服务器地址>:8788/` 会跳转过去）。启动日志里有一行 `panel listening`，带实际监听的地址。

- 这个端口上只有面板：`/api/v1/*`、`/healthz`、`/metrics` 都返回 404，它们仍然只在 `server.listen` 上。
- 设了 `panel.listen` 后，`server.listen` 上不再有 `/panel/`（返回 404）。用 SSH 隧道时转发面板的端口，例如 `ssh -L 8788:127.0.0.1:8788 <用户>@<服务器>`，再打开 `http://127.0.0.1:8788/panel/`。
- `panel.listen` 不能和 `server.listen` 是同一个端口；端口被占用时核心启动失败。
- 这个端口上每个请求（含请求体）必须在 30 秒内读完，否则连接被断开，免得慢速客户端长期占着连接。

**安全警告**：面板自己不提供 HTTPS。经普通 HTTP 从公网访问时，登录密码和会话 cookie 都以明文经过网络，路上的任何人（同一 Wi-Fi、运营商、中间节点）都可能看到并冒用，拿到 cookie 就等于拿到管理员权限。

- 推荐在前面放 HTTPS 反向代理，并设 `cookie_secure: true`，做法见 [部署：经 HTTPS 反向代理访问](deployment.md#经-https-反向代理访问)。这时让 `panel.listen` 只监听本机（如 `127.0.0.1:8788`），代理转发到这个端口。
- 至少要用防火墙只放行自己的 IP 访问这个端口，见 [部署：用单独端口直接访问](deployment.md#用单独端口直接访问)。
- 登录限流有一个全局上限（全部来源合计 15 分钟内失败 30 次，见下文 [登录与安全](#登录与安全)）。端口对整个互联网开放时，任何人乱试密码都能让这个上限用尽，在最多 15 分钟内谁都无法登录（包括你自己）；已经登录的会话不受影响，可以继续使用。

## 页面

界面是中文，跟随系统的深浅色，窄屏（手机）上单列显示。未登录时任何页面都会跳到登录页；使用中途会话过期时，登录后回到原来的页面。

- **概览**：今天（`server.timezone` 的当地日）的调用数、失败数和各类 token，最近 7 天按模型的 token 柱状图，版本和启动时间，实时状态，以及各 bot 的适配器最近一次调用核心的时间。每 10 秒刷新一次。
  - 实时状态包括准入队列深度、发件箱待发数、向量积压、关键词索引补建剩余、熔断中的角色。没有配置 `embedding` 段时向量积压显示“未启用”（接口里是 `null`）。
  - “最近调用”是绑定这个 bot 的 API 令牌最近一次通过鉴权的时间（通常就是它的适配器），只记在内存里，从本次进程启动起算：核心刚重启、适配器还没来得及调用时显示“自本次启动未调用”。
  - 页面下方的“进程指标”是本进程 Prometheus 指标的快照，重启后清零；需要长期曲线时请另接 Prometheus。
- **用量**：按日期范围（最近 7、30、90 天或自定义）和分组（模型、角色、用途、bot）统计每天的调用数、失败数和 token，画每日堆叠柱状图，下面是区间合计表和逐次调用明细。统计规则见下文 [模型用量](#模型用量)。
- **聊天**：和 bot 私聊，并管理面板 bot。地址是 `#/chat`，打开某个聊天时是 `#/chat/<会话键>`（会话键经 URL 编码），刷新页面或打开这个链接都会回到同一个聊天。以前会话详情里“聊天”标签的链接 `#/conversations/<会话键>/chat` 会自动换成这个地址，旧书签照样能用。
  - 左栏按 bot 分组列出可以聊的私聊，组的顺序同 `GET bots`（先配置 bot，再按新建顺序列面板 bot），组内最近活动的在前，每个 bot 最多 200 个，群聊不列出。每项是对方的名字和最后一条消息的时间（今天的显示时刻，更早的显示日期）。面板里新建的聊天直接列出，QQ、Telegram 上的私聊标“接管”，用法见下文 [在面板里聊天](#在面板里聊天)。
  - 组标题是 bot 的名字。配置文件里的 bot 标“配置”，注明“在配置文件里修改”；面板 bot 的组标题旁有“编辑”“删除”，引用的模板或技能已经不在时在标题下面标出（见 [面板 bot](#面板-bot)）。每组下面有“新建聊天”，左栏最上面有“新建 Bot”。
  - 左栏在打开聊天页时读取一次，新建 bot、编辑、删除和新建聊天之后重新读取；切换聊天只换右栏，左栏不重新读取，也不定时刷新。平台上来了新的私聊时，点左栏上方的“刷新”。
  - 右栏是选中的聊天：标题是对方的名字和 bot，接管的聊天带“接管”标记，“会话详情”链接到这个会话的页面。还没选聊天时显示提示。
- **会话**：所有 bot 的全部群聊和私聊，带消息数、最后一条消息的时间、是否暂停、主动发言和记忆开关、人设模板和版本。私聊的主动发言徽章显示主动找人的档位，如“主动：中”；总开关或档位关闭时显示“主动关”。右上角的“新建聊天”在面板里和 bot 开一个私聊，建好后在聊天页打开，见下文 [在面板里聊天](#在面板里聊天)。点进一个会话默认打开“消息”标签；私聊会话的详情里有“在聊天页打开”，面板里新建的聊天还有“删除这个聊天”。有以下标签页：
  - **消息**：按时间倒序浏览，从面板发出或送到面板的消息带“面板”标记；也可以输入关键词检索，规则和 `recall` 的关键词检索相同（任一词命中，按相关度排序，同分时新的在前），可以限定日期。
  - **策略**：常用字段做成了表单（主动发言、记忆、历史条数、分条上限 `reply.max_parts`，记忆的摘要和笔记字数上限 `memory.summary_max_chars`、`note_max_chars`（0 表示按会话类型的默认值，提示里写出本会话的默认值和当前生效的值）；私聊会话还有“私聊等待” `reply.private_wait` 和“私聊最长等待” `reply.private_max_wait`，写成 `6s`、`500ms`、`1m` 这样的时长，两项都填 0 表示用默认值；以及“主动找人档位” `proactive.private.level`（留空按“中”）和“免打扰开始”“免打扰结束” `proactive.private.quiet_start`、`quiet_end`，写成 `23:00` 这样的两位数时间，按 `server.timezone`，两项要么都填、要么都留空，都留空时按对方的作息推算，见 [配置：私聊主动找人](configuration.md#群策略)），另有“高级：JSON 编辑”可以整份替换；保存时服务端校验，不合法的值会在表单下方显示服务端给出的错误；暂停、恢复；历史版本列表，可以把任一版本恢复为新版本。字段含义见 [使用指南：私聊的节奏](usage.md#私聊的节奏)。
  - **人设**：编辑人设的各个字段、套用模板（配置文件里的和面板里改过或新建的，见 [人设模板和外观参考图](#人设模板和外观参考图)）、回滚到历史版本。会话还没有人设时只显示“尚未生成”和将要使用的模板，查看不会生成第 1 版。
    - “内容尺度”一栏有“未成年角色”和“成年人设”两个开关，以及“内容规则”“记忆规则”两个文本框，对应人设的 `minor`、`adult`、`content_rules`、`memory_rules`。勾选“未成年角色”后，“成年人设”会被取消并且不可用；服务端同样拒绝两者同时为真。两段规则只在开放模式（成年、非未成年、私聊）下生效，规则见 [配置：内容模式](configuration.md#内容模式)。
    - 页面顶部显示“私聊内容模式”：当前人设（还没有人设时是将要使用的模板）放在私聊里会是开放还是保护模式。群聊不论人设如何都是保护模式。
    - “状态条”一栏编辑人设的 `bars`：每行是名称、基准、半衰期（写成 `6h`、`90m` 这样的时长）和说明，可以添加和删除，最多 4 行。一行都没有时保存的人设不带 `bars`，运行时用默认的心情和精力。限制见 [配置：状态条](configuration.md#状态条)。
    - “外观”一栏：“外观描述”对应 `appearance`（最多 800 字，不为空时作为“外貌”一段写进系统提示）。参考图显示 `appearance_image` 的缩小图，下面有三个按钮：“上传图片”（PNG 或 JPEG，选好后先在本地预览，同时上传到服务器，成功后显示服务器上的缩小图、原图尺寸和回复时约占的 token）；“生成外观介绍”（让识图模型看这张图，把结果填进外观描述框，框里已有内容时先确认，最多约 40 秒）；“移除图片”。“回复时附带外观图”默认勾选，取消勾选保存为 `appearance_image_skip: true`。上传、生成和移除都只改表单，点保存才写进人设。参考图读取失败（比如已被清理）时提示重新上传或移除：引用不存在的图片时服务端拒绝保存。
    - 人设模板页用同一个表单，见下面的“人设模板”。
  - **记忆**：摘要、整理进度和最近的错误，成员的备注和好感度，可以修改备注或好感度、忘记某个成员、立即整理一次，也可以查看某个成员（或摘要）的修订记录。这里的数据和 Bearer API 的 `GET /api/v1/conversations/{id}/memory` 完全相同：被忘记的成员没有备注和好感度，不会出现在列表里。
    - 编辑成员时，备注框下方写出本会话当前生效的备注字数上限（`note_max_chars`），超过时服务端拒绝保存。
    - 编辑成员时，好感度输入框下方有“-5”“+5”“+10”“设为 100”四个快捷按钮：按已保存的好感度（还没有时从初始值 20 起算）计算新值，限制在 0–100，点击后立即保存，只改好感度；原因取“原因”框的内容，留空时记为“快捷调整 +5”这样的文字。备注或好感度框里有未保存的修改时先弹窗确认（这些修改会丢失），取消则不保存。保存过程中四个按钮都不可用。结果已经是当前值时只提示“好感度已经是 N”，不写入。
  - **自主**：只有私聊有。机器人在这个私聊里的状态、状态条和主动找人的情况，数据同 [HTTP API：自主状态](api.md#自主状态)；写入后页面重新读取。
    - 当前状态：在做什么、开始和预计结束的时间。“结束状态”按钮确认后立即结束它，同时取消回来说话的定时。
    - 状态条：回落到现在的值（用进度条显示）、档位词和基准值。输入 0–100 的整数和可选的原因后保存，直接设成这个值，不受模型调整的单次和每日上限约束，变化记录的来源为 `panel`。
    - 最近变化：最近 50 次状态条变化的时间、条名、前后值、原因和来源（模型、面板或 API）。
    - 主动找人：总开关、档位（策略没设时注明按默认）、今天已发起几条和上限（“今天 1/2”）、免打扰时段及其来源（设定、按作息推算，或消息不够推算时的默认值）、对方再次发言前还能发起几条（发出的回来说话也占一条），以及还没触发的定时（回来说话、主动找人、追问）和时间。这几项在“策略”里修改。面板新建的聊天注明不会主动找人和追问。
    - 状态内容和变化原因是模型或管理员写的文字，按纯文本显示。
  - **任务**：这个会话的任务。
- **人设模板**：数据库里的模板，以及没有保存过 `default` 时内置的默认人设，规则见 [人设模板和外观参考图](#人设模板和外观参考图)。地址是 `#/templates`，打开某个模板时是 `#/templates/<模板名>`。
  - 列表：模板名、人设名、角色名、来源（数据库／内置）、有没有参考图、哪些 bot 把它当默认模板（面板 bot 显示为“名字（面板）”）、最后修改的人和时间。
  - 点进一个模板，用和会话“人设”页相同的表单编辑（包括外观描述、参考图和状态条），点“保存”写入。
  - `default` 有“恢复为内置默认”，没有保存过时不可用；其他模板有“删除”，还有 bot 用它作默认模板时显示服务端的说明，不删除。
  - 所有模板都有“复制为新模板”：输入新名字（1–32 个小写字母、数字、`-` 或 `_`，不能和已有模板重名），用表单里当前的内容（包括还没保存的修改）新建一个面板模板，原模板不变，然后打开新模板。新建模板也是这样：打开一个模板，复制后再改。
  - 页面提示：改模板不影响已经在用它的会话（会话第一次回复时复制一份模板，之后各自独立）；要更新已有会话，到会话的“人设”页点“套用模板”。
- **任务**：全部任务，可以按状态、bot、会话筛选；任务详情里有 meta、生成的动作，以及这个任务的每次模型调用。任务受 `runtime.ops_retention`（默认 30 天）清理，更早的查不到。
- **审计**：审计记录，可以按种类和 bot 筛选，受 `runtime.audit_retention`（默认 90 天）清理。
- **配置**：当前生效的配置（环境变量展开以后），只读。见下文 [配置页打码](#配置页打码)。

所有用户数据（消息、昵称、人设、审计内容等）都按纯文本显示，里面的 HTML 不会被执行。

### 在面板里聊天

在“聊天”页可以直接和 bot 私聊，有两种用法：

- **接管已有私聊**：聊天页左栏里标“接管”的是平台（QQ、Telegram）上的私聊，私聊会话详情里的“在聊天页打开”也会打开它。在这里发消息，发言人是这个会话的用户（最近一条用户消息的平台用户 ID 和昵称）；会话里还没有用户消息时不能发，返回 400。
  - 接管时发的消息不算这个用户本人的（[私聊里主动找你](usage.md#私聊里主动找你)）：不参与推算他的免打扰时段，也不算他开口，所以不会让机器人对他多出可以主动发的条数。面板消息照样取消还没发出的主动消息，并重新排下一次。
  - 回复面板消息的那一轮不提供 `set_status`：否则到点回来说话会发到原平台，对这个用户说起他没见过的对话。想试状态，用面板里新建的聊天。
- **新建聊天**：在聊天页某个 bot 那一组下点“新建聊天”（选好了这个 bot），或者在会话列表点“新建聊天”，选择 bot（面板 bot 显示为“名字（面板）”，配置 bot 显示为“名字（ID）”）、填写 bot 对你的称呼（留空时用面板用户名，最长 32 字），提交后在聊天页打开新聊天。新会话的平台是 `panel`，会话键形如 `qq-main:panel:private:panel_<随机>`，发言人 ID 是 `panel_<面板用户名>`。这个发言人是该 bot 的管理员，可以用管理员命令；私聊白名单和 `private_chat` 开关对 `panel` 平台不生效。

两种用法的共同点：

- 消息和平台发来的消息走同一套流程：输入审查、限流、私聊的等待节奏都照常生效。消息长度 1 到 2000 字，只能在私聊里发，群聊返回 400。
- 从面板发出的消息记为 `panel` 通道，带“面板”标记。由它触发的回复也走 `panel` 通道：只在面板里显示，不会发到原平台，适配器拉取发件箱时也拿不到。之后从原平台发来的消息照常在原平台回复，上下文里包括在面板里聊过的内容。面板消息和平台消息在等待期间合并成同一个任务时，以最新一条消息定通道：平台消息之后紧接着在面板里发一句，这次回复就只出现在面板里。
- 打开一个聊天时显示最近 50 条消息，之后每 1.5 秒拉一次新消息，切到别的聊天或离开聊天页就停止；聊天已经不存在（被删除，返回 404）时也停止，只显示一句“这个聊天已不存在”，不能再发消息。bot 自己的消息在左，用户的在右。会话有排队中或运行中的任务，或者面板通道还有没送达、也没过期的分条时，显示“对方正在输入…”。
- 回车发送，Shift+回车换行。发送失败时显示服务端返回的错误，输入框里的内容保留。

面板里新建的聊天详情页有“删除这个聊天”按钮，确认后删除这个会话键下的全部数据（消息、任务、动作、记忆、人设、策略等），只保留用量统计（`model_calls`）和审计记录。平台上的会话不能在面板里删除。

- 删除前先取消这个聊天里排队和正在生成的任务，最多等 5 秒让它们停下；没停下时什么都不删，返回 409 `CHAT_BUSY`（`retryable: true`），稍后重试即可。
- 被 bot 忽略的消息不存正文、也没有任务，只留一条事件记录，删除时认不出它属于这个聊天；这些记录留到事件的保留期（`runtime.idempotency_retention`）满了由清理任务删除。
- 用量统计（`model_calls`）和审计记录按设计保留，分别到 `runtime.usage_retention`、`runtime.audit_retention` 到期清理。
- 删除时如果这个聊天的记忆整理或语义索引（`embedding`）正好在进行，它们可能在删除之后再写入几行记忆或向量数据。这些行挂在已删除的会话键下，会话键不会再被使用（新建聊天总是用新的随机 ID），所以不会出现在任何会话里，只占少量空间。

### 面板 bot

面板 bot 是在面板里新建、只在面板里聊天的 bot：有自己的名字、默认人设模板、技能，每个聊天有自己的人设、记忆和策略；不接任何平台，也不能发网关令牌。配置文件里的 bot 在面板里只读，要改就改配置文件。用法见 [使用指南：面板 bot](usage.md#面板-bot)，设计见 `docs/superpowers/specs/2026-11-15-panel-bots-design.md`。

- **在哪里管理**：在“聊天”页。左栏最上面的“新建 Bot”打开表单：名字、默认人设模板（下拉框，选项是 `GET bots` 的 `templates`，新建时选 `default`）、技能（复选框，列出 `GET bots/skills` 的技能和说明，新建时全选）。名字和别的 bot（包括配置 bot）重名时，表单里提示一句，不阻止保存。面板 bot 组标题旁的“编辑”打开同一个表单；“删除”打开确认框，写明会删掉几个聊天，输入和 bot 一样的名字后“删除”按钮才能点。删除返回 409 时确认框里说明原因、可以再点一次重试，左栏重新读取（可能已经删掉了一部分聊天）。正在看的聊天属于被删的 bot 时，右栏回到提示；删除返回 409 时，正在看的聊天已经被删掉的话右栏也回到提示。
- **ID**：服务自动生成，`panel-` 加 8 位小写字母和数字，如 `panel-k3x9q2ma`；用过的 ID（包括已删除的 bot 的）不会再用。配置文件里的 bot ID 不能以 `panel-` 开头，所以两类 ID 不会冲突。
- **校验**：名字去掉首尾空白后 1–32 字，不能有控制字符、换行，以及零宽字符、方向控制符这类不可见的格式字符（Unicode 的 Cf 类），可以和别的 bot 重名；默认人设模板必须存在，不填时用 `default`；技能必须已加载、允许在私聊里用、用到的工具都已注册，可以一个都不选，重复的只算一次。最多 50 个面板 bot。不通过时返回 400，`message` 是中文原因，如“名字不能为空”。
- **生效**：新建后立即出现在所有列出 bot 的地方（新建聊天、概览、会话、用量等），不用重启。改名字和技能从下一次回复起生效；默认人设模板只影响之后新建的、以及 bot 还没回复过的聊天，已有聊天的人设在会话的“人设”页里改。
- **重启后**：面板 bot 存在数据库里，启动时载入。引用的人设模板已经不存在时，运行时改用 `default`；某个技能不存在或不能在私聊里用时，运行时忽略它。两种情况都记一条警告日志，接口的 `problems` 里给出说明（如“人设模板“x”已不存在，暂用 default”），不影响启动；编辑并保存一次后说明消失。
- **删除**：请求要带上 bot 的名字确认，不一致返回 400。删除时先把 bot 标为“删除中”，这期间新建聊天、发消息、编辑和再次删除都返回 409 `CHAT_BUSY`；然后逐个删除它的聊天，规则同上面的“删除这个聊天”。
  - 某个聊天里被取消的任务 5 秒内没停下时，删除停在这里，返回 409 `CHAT_BUSY`（`retryable: true`）：已删的聊天不恢复，bot 保留、不再是“删除中”，可以重试。
  - 聊天都删完后，删掉 bot 和它名下残留的用户、事件记录。`bot_instances` 里的行（存着名字）、模型用量和审计记录保留，用量页按 bot 分组时把它显示为“名字（已删除）”。
- **审计**：新建、编辑、删除分别写 `panel.bot.create`、`panel.bot.update`、`panel.bot.delete`，`actor` 为 `panel:<用户名>`，`bot_instance_id` 和目标都是这个 bot 的 ID。内容是保存的名字、默认人设模板和技能；删除时是名字和删掉的聊天数 `chats`。

### 人设模板和外观参考图

人设模板只存在数据库里，在面板里修改和新建，人设可以带一张外观参考图。页面见上文的“人设模板”和会话的“人设”标签页。规则见 [配置：人设模板](configuration.md#人设模板)，设计见 `docs/superpowers/specs/2026-11-15-persona-templates-appearance-design.md`。

- **模板目录**：数据库里的模板，来源 `source` 为 `db`；数据库里没有 `default` 时加上内置的默认人设，来源 `builtin`。旧配置文件里的 `persona_templates` 在升级后第一次启动时导入数据库（见 [配置：人设模板](configuration.md#人设模板)）。改动立即生效，不用重启，重启后还在。
- **修改和新建**：`PUT persona-templates/{name}` 写入整份人设，覆盖同名模板，或者新建模板，新名字只能是 1–32 个小写字母、数字、`-` 或 `_`。内容的校验和会话人设相同（字数上限，`minor` 和 `adult` 不能同时为真，`appearance_image` 必须是已上传的图片），不通过返回 400。
- **恢复和删除**：`default` 只能“恢复为内置默认”（`POST …/reset`，删掉数据库里的 `default`），不能删除，删除返回 403。其他模板只能删除，恢复返回 400；还有 bot（配置 bot 或面板 bot）用它作默认模板时返回 409 `TEMPLATE_IN_USE`，`message` 列出这些 bot（如“bot 织语（qq-main）、小红 还在用这个模板”，配置 bot 带 ID），先把这些 bot 改用别的模板。
- **已有会话不变**：会话第一次回复时把模板复制成自己的人设，之后各自独立。改模板只影响之后新建的会话；要更新已有会话，在会话的“人设”页套用模板。
- **bot**：模板写入、恢复或删除后，配置 bot 和面板 bot 按新的目录重新生效：引用的模板重新出现时，“人设模板“x”已不存在”的说明随之消失。
- **上传图片**：`POST persona-images` 的请求体就是图片文件本身（不是表单），最多 8 MiB，超过返回 413。格式按文件开头的字节判断，不看 `Content-Type`，只接受 PNG 和 JPEG；宽和高都不能超过 8000 像素、总共不超过 2500 万像素，先读文件头检查尺寸、再解码，不合格返回 400，什么都不存。有透明部分的 PNG 铺在白底上；JPEG 的方向标记（EXIF orientation）不处理，按像素原样使用。服务器存原图，另存一份长边不超过 768 像素、质量 85 的 JPEG 给回复模型用，返回 201 `{"id","width","height","reply_tokens"}`：`id` 是原图内容的 SHA-256（同一张图再传一次得到同一个 ID），`reply_tokens` 是缩小图按 `ceil(宽/28) × ceil(高/28) + 2` 估算的 token。
- **取图**：`GET persona-images/{id}` 默认返回缩小图（`image/jpeg`），`?size=original` 返回原图（按上传时识别的类型）。
- **生成外观介绍**：`POST persona-images/{id}/describe` 把原图缩到长边不超过 1536 像素发给识图模型 `model_profiles.vision`（见 [配置：model_profiles](configuration.md#model_profiles)），返回 `{"text"}`，不自动保存，确认后写进人设的“外观描述”再保存。没有配置 `vision` 时返回 503 `MODEL_UNAVAILABLE`“未配置识图模型”（`retryable: false`，重试没有用，要改配置）；40 秒内没有完成返回 504（`retryable: true`）、模型调用失败返回 502，都是 `MODEL_UNAVAILABLE`，细节只写日志；图片不存在返回 404。用量按用途 `vision` 记录。
- **一次一张**：解码一张大图要几百 MB 内存，所以上传和生成外观介绍各自同时只处理一个请求；另一个请求同时到达时直接返回 409 `CHAT_BUSY`（`retryable: true`）“正在处理另一张图片，请稍后”，不排队。
- **清理**：没有被任何模板或会话人设版本（包括历史版本）引用的图片，上传满 7 天后由每小时的维护任务删除。刚上传、还没保存进人设的图片有这 7 天的余地；同一张图再上传一次重新计时。
- **审计**：模板写入、恢复、删除分别写 `persona.template.put`、`persona.template.reset`、`persona.template.delete`，目标是模板名，`put` 的内容是写入后的来源 `source`、是否新建 `created` 和原因 `reason`。上传写 `persona.image.upload`，目标是图片 ID，内容是类型、宽高、字节数和 `reply_tokens`。生成外观介绍成功时写 `persona.appearance.describe`，目标是图片 ID，内容只有生成文字的字数 `chars`。都不属于某个 bot（`bot_instance_id` 为空），图片内容和生成的文字都不进审计和日志。

### 面板上的修改

面板上的修改和 Bearer API、聊天命令走同一套代码，校验规则、字数上限、好感度范围完全一致，校验失败时原样显示服务端返回的错误。

- 每次修改都写一条审计记录，`actor` 为 `panel:<用户名>`，`bot_instance_id` 是会话所属的 bot。种类沿用 API 的 `policy.update`、`persona.update`、`memory.update`、`memory.forget`、`autonomy.buff`（修改私聊的状态条）、`autonomy.status.clear`（结束私聊的状态）；触发整理写 `memory.consolidate`（API 触发整理不写审计）。登录成功写 `panel.login`。
- 面板聊天的审计：发消息写 `panel.message`（内容只有消息 ID，不含正文），新建聊天写 `panel.chat.create`，删除聊天写 `panel.chat.delete`；目标都是会话键。
- 面板 bot 的新建、编辑、删除写 `panel.bot.create`、`panel.bot.update`、`panel.bot.delete`，目标是 bot 的 ID，见 [面板 bot](#面板-bot)。
- 人设模板的写入、恢复、删除写 `persona.template.put`、`persona.template.reset`、`persona.template.delete`，目标是模板名；上传参考图写 `persona.image.upload`，生成外观介绍写 `persona.appearance.describe`，目标是图片 ID；这几种的 `bot_instance_id` 为空，见 [人设模板和外观参考图](#人设模板和外观参考图)。
- 策略和人设的新版本里 `updated_by` 为 `panel:<用户名>`；成员记忆的修订记录、状态条的变化记录来源为 `panel`。
- 忘记成员等同于这个人在会话里发 `/忘记我`；暂停、恢复和 `/pause`、`/resume` 写入同样的策略版本（恢复时并发回到暂停前最近一个非 0 的值）。

## 登录与安全

- **会话**：登录后有效期 `session_ttl`（默认 168h），超过 `idle_timeout`（默认 12h）没有操作也会失效，以先到者为准。“登出”立即删除会话。
- **改密码**：在配置里换掉某个用户的 `password_hash` 并重启后，这个用户已有的会话全部失效，需要用新密码重新登录；从配置里删掉一个用户，他的会话同样失效。
- **防暴力破解**：同一来源 IP 15 分钟内登录失败 5 次，或全部来源合计 15 分钟内失败 30 次，之后的登录直接返回 429 并带 `Retry-After`（秒），等最早的失败移出 15 分钟窗口后恢复。还在校验中的登录也计入，所以并发尝试也无法突破上限。来源 IP 只取 TCP 连接的对端地址，不信任 `X-Forwarded-For`；经反向代理访问时所有人都是代理的地址，共用同一个“每 IP 5 次”的额度。用户名不存在时也做同样成本的哈希计算，从响应时间看不出用户名是否存在。
- **cookie**：名为 `conloom_panel`，`HttpOnly`（脚本读不到）、`SameSite=Strict`、`Path=/panel/`；`panel.cookie_secure: true` 时另加 `Secure`，浏览器只经 HTTPS 发送。数据库只存会话令牌的 SHA-256。
- **CSRF**：除登录外，所有 `POST`、`PUT`、`DELETE` 请求都必须带 `X-CSRF-Token` 头，值从 `GET /panel/api/session` 取得，缺少或不对返回 403。请求带 `Origin` 头时（登录也一样），它的主机部分必须和 `Host` 头一致，否则返回 403；所以反向代理必须原样转发 `Host` 头。
- **响应头**：所有 `/panel/` 响应都带严格的 `Content-Security-Policy`（只允许本站资源、禁止被嵌入框架）、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`；接口响应另加 `Cache-Control: no-store`。
- **日志**：登录失败记一条 warn 日志 `panel login failed`，带来源 IP，用户名只在它存在时记录，从不记密码；失败次数计入 `conloom_panel_login_failures_total`。面板接口不记请求体和消息内容。

### 配置页打码

配置页按白名单输出：只输出明确列出的非敏感字段，以后新加的配置字段在列入之前不会显示。

- 所有密钥只显示 `(已设置)` 或 `(未设置)`，不输出值：模型和向量服务的 `api_key`，适配器的 `access_token`、`token`、`core_token`，面板用户的 `password_hash`。
- `api_tokens` 只显示名字、所属 bot 和 scope。
- `model_profiles.*.extra_body` 只显示键名；布尔值和数字照常显示，字符串、列表、对象等其他值一律显示为 `(已设置)` 或 `(未设置)`，因为它们可能藏着凭据。
- 接口地址（模型和向量服务的 `endpoint`，适配器的 `ws_url`、`core_url`、`api_base`）显示主机和路径，但地址里可能带凭据：`user:password@` 部分去掉，查询参数只保留参数名、值一律显示为 `(已隐藏)`，`#` 后面的部分也显示为 `(已隐藏)`；解析不了的地址只显示 `(已设置)`。
- 默认群策略、私聊策略整段原样显示，它们只有行为设置，不含凭据。人设模板不在配置里，不显示。
- 没有配置 `embedding` 段时，`embedding` 显示为 `null`。

## 模型用量

每次模型调用都在 `model_calls` 表里记一行，重启不丢，面板的用量页和概览从这里统计。

- **记哪些调用**：
  - `chat`：回复（包括带工具的回复里的每一轮请求，每轮一行），成功和失败都记；
  - `memory`：记忆整理；
  - `embed_index`：后台为语义检索生成向量；
  - `embed_query`：检索时把查询句转成向量；
  - `vision`：识图模型生成外观描述（标签"识图"），没有 bot 和会话。
- **每行的内容**：时间、bot、会话、任务（记忆整理和后台生成向量没有任务时为空）、用途、角色（如 `short_context`，向量调用记 `embedding`）、模型名（上游返回的；失败拿不到时用配置里的）、提示 / 回复 / 思考 token（上游没返回就记 0）、发请求前估算的输入 token（只有 `chat` 有）、耗时、结果和上游的结束原因（`finish_reason`）。结果是 `ok`、`error`、`timeout`、`canceled` 之一，`ok` 以外的都算失败。
- **不拖慢回复**：记录先放进内存队列（4096 条），后台攒够 200 条或每 2 秒一次批量写库。队列满时丢弃并计入 `conloom_usage_dropped_total`，写库失败记 warn 日志并计入 `conloom_usage_write_errors_total`；退出时最多等 5 秒把剩下的写完，超时没写完的丢弃并记 warn 日志，不影响正常退出。原有的 `model call` 日志和 `conloom_model_*` 指标不变。
- **保留**：`runtime.usage_retention`，默认 `9600h`（400 天），由每小时的清理任务分批删除，删除条数计入 `conloom_cleanup_deleted_total{table="model_calls"}`。
- **统计规则**：
  - 按 `server.timezone` 的自然日分桶；日期范围最长 400 天，结束日期是今天时包含今天到目前为止的调用。
  - 失败数是结果不为 `ok` 的调用数。
  - 延迟 p50 / p95 只按成功的调用计算，并且只在范围不超过 90 天时计算，更长的范围只给次数和 token。
  - 日期格式错误、开始晚于结束、超过 400 天都返回 400。
  - `runtime.usage_retention` 比所选范围短时，更早的日子显示为 0。
  - 按 bot 分组时，已经不在 bot 目录里的 bot（删除的面板 bot，或从配置文件里去掉的 bot）显示为“名字（已删除）”，名字取自 `bot_instances`；同一范围里有两个这样同名的 bot 时，后一个再加上它的 ID。
- **体积**：每次调用连同索引占几百字节，其中为统计查询建的覆盖索引每 100 万行约 97MB。按每天几千次调用算，保留 400 天是一两百万行，几百 MB；调用很多时可以调短 `runtime.usage_retention`。

## 接口

页面用的 JSON 接口都在 `/panel/api/` 下，带登录 cookie 访问；这些接口为面板服务，不保证兼容性，脚本和程序请用 [HTTP API](api.md)。

- 错误格式和 Bearer API 相同：`{"error":{"code","message","retryable","trace_id"}}`。没登录或会话已失效返回 401 `UNAUTHORIZED`，CSRF 或 `Origin` 检查不通过返回 403 `FORBIDDEN`，登录被限流返回 429 `RATE_LIMITED`。
- 时间是 RFC3339 UTC 字符串，可以为空的时间为 `null`。用量接口的 `from`、`to` 是 `server.timezone` 的当地日期 `YYYY-MM-DD`。
- 路径里的 `{key}` 是会话键 `bot:平台:类型:会话ID`（如 `qq-main:qq:group:group_10001`）经 URL 编码后的形式。面板跨 bot，所以不用 Bearer API 里那种按令牌绑定 bot 的会话 ID。
- 任务列表、审计记录、调用明细（`usage/calls`）和消息浏览用 `before` 游标加 `limit` 翻页；响应里的 `next_before` 是下一页的 `before`，最后一页为 `null`。消息检索改用 `offset` 翻页（见下表）。`limit` 都是默认 50、最大 200。会话列表不分页，一次返回全部会话。

**鉴权**

| 接口 | 说明 |
|---|---|
| `POST login` | 请求 `{"name","password"}`，成功返回 `{"user","csrf_token","expires_at"}` 并设置 cookie |
| `GET session` | 当前用户、`csrf_token`、过期时间 |
| `POST logout` | 删除会话、清掉 cookie，返回 204 |

**读接口**

| 接口 | 说明 |
|---|---|
| `GET overview` | 版本、提交、启动时间、时区；各 bot（带 `source`，见 `GET bots`）及其 `last_seen_at`；实时状态 `live`；今天的用量 `today` |
| `GET usage?from=&to=&group=` | 每日分桶和区间合计；`group` 为 `model`（默认）、`role`、`purpose`、`bot`；不传日期时为截至今天的 7 天。`bots` 按 bot ID 给出每个 bot 的 `name` 和 `deleted`：bot 目录里的 bot 用当前的名字、`deleted` 为 false；只在 `bot_instances` 里登记过的（删除的面板 bot、从配置文件去掉的 bot）用登记的名字、`deleted` 为 true |
| `GET usage/calls?from=&to=&model=&purpose=&conversation=&before=&limit=` | 调用明细，新的在前；`conversation` 是会话键 |
| `GET metrics/live` | 本进程 Prometheus 指标的快照：计数器和仪表给值，直方图给次数、总和和按桶估算的 p50 / p95 |
| `GET bots` | 全部 bot `bots`：先配置 bot，再按新建顺序列面板 bot。每项 `id`、`name`、`source`（`config` 或 `panel`）、`persona_template` 和 `skills`（运行时生效的值）、`problems`（失效引用的说明，如配置 bot 或面板 bot 的模板已不存在，没有时为空列表）、`chats`（私聊数）、`last_message_at`（这些私聊最后一条消息的时间）。另有 `templates`：可选的人设模板名，按名字排序 |
| `GET bots/skills` | 面板 bot 可以启用的技能 `skills`（已加载、允许在私聊里用、工具齐全），每项 `name`、`description`，按名字排序 |
| `GET chats` | 聊天页左栏用：`bots` 按 bot 分组，顺序同 `GET bots`，每组 `id`、`name`、`source` 和这个 bot 的私聊 `chats`（没有时为空列表）。私聊按最近活动从新到旧：取最后一条消息的时间和会话建立的时间中较晚的一个，所以刚新建、还没有消息的聊天排在最前；排好序后每个 bot 取前 200 个；群聊不列出。每项 `key`、`title`（对方的显示名：会话里他最新的昵称；面板聊天还没人发言时是新建时填的称呼；都没有时是平台会话 ID）、`platform`、`takeover`（平台上的私聊为 true，在面板里发言是接管；面板聊天为 false）、`last_message_at` |
| `GET conversations` | 全部会话 `conversations`，以及全部 bot `bots`（`id`、`name`、`platform`、`source`，“新建聊天”从这里选；面板 bot 的 `platform` 和 `source` 都是 `panel`）。私聊会话的 `proactive_level` 是生效的主动找人档位（`off`、`low`、`mid`、`high`，没设时为 `mid`），其他会话为空字符串；单个会话的接口同样有这个字段 |
| `GET conversations/{key}` | 单个会话 |
| `GET conversations/{key}/messages?before=&limit=` | 浏览消息，按 `seq` 游标翻页。每条消息带 `channel`：`panel` 表示从面板发出或送到面板，平台上的消息为空字符串 |
| `GET conversations/{key}/messages?q=&from=&to=&offset=&limit=` | 检索消息，按 `offset` 翻页，响应里的 `next_offset` 是下一页 |
| `GET conversations/{key}/chat?after=` | 面板聊天的增量拉取：`seq` 大于 `after`（默认 0）的消息，从旧到新，每次最多 200 条，格式同消息浏览；`typing` 为 true 表示会话有排队中或运行中的任务，或面板通道还有没送达、也没过期的分条；`next_after` 是下一次的 `after`（没有新消息时等于本次的 `after`） |
| `GET conversations/{key}/policy` | 当前策略、历史版本和 `memory_limits`（`summary_max_chars`、`note_max_chars` 是当前生效的记忆字数上限，`default_summary_max_chars`、`default_note_max_chars` 是本会话类型的默认值，策略里写 0 时用它们） |
| `GET conversations/{key}/persona` | 当前人设、历史版本、可用模板；还没有人设时给出将要使用的模板，不写库。`minor` 是当前人设（没有时是该模板）的 `minor`，`content_mode` 是它在私聊里的内容模式（`open` 或 `protected`），与会话本身是群聊还是私聊无关 |
| `GET conversations/{key}/memory` | 与 `GET /api/v1/conversations/{id}/memory` 相同 |
| `GET conversations/{key}/memory/revisions?user_id=` | 某成员的修订记录，不传 `user_id` 时是摘要的 |
| `GET conversations/{key}/autonomy` | 私聊的状态、状态条、最近的状态条变化和主动找人的情况，与 `GET /api/v1/conversations/{id}/autonomy` 相同（见 [HTTP API：自主状态](api.md#自主状态)）；不是私聊返回 400 |
| `GET persona-templates` | 人设模板目录 `templates`，按名字排序。每项 `name`、`source`（`db` 或 `builtin`）、`persona_name`、`character_name`、`appearance_image`（没有参考图时为空字符串）、`users`（运行时用它作默认模板的 bot，每项 `id`、`name`、`source`，没有时为空列表）、`updated_by` 和 `updated_at`（最后写入的人和时间，内置的 `default` 为空字符串和 `null`） |
| `GET persona-templates/{name}` | 单个模板：列表里的字段，加上整份人设 `persona`；不存在返回 404 |
| `GET persona-images/{id}?size=` | 图片本身：`size` 为 `reply`（默认，缩小图，`image/jpeg`）或 `original`（原图，`image/png` 或 `image/jpeg`）；其他值返回 400，图片不存在返回 404 |
| `GET tasks?status=&bot=&conversation=&before=&limit=` | 任务列表 |
| `GET tasks/{id}` | 任务详情：任务、meta、动作、模型调用 |
| `GET audit?kind=&bot=&before=&limit=` | 审计记录 |
| `GET config` | 打码后的当前配置 |

**写接口**（都要带 `X-CSRF-Token`，都写审计）

| 接口 | 说明 |
|---|---|
| `POST bots` | 新建面板 bot，请求 `{"name","persona_template","skills"}`（`persona_template` 省略时为 `default`，`skills` 省略时不启用技能），返回 201 和新 bot，格式同 `GET bots` 的一项；校验不通过返回 400 |
| `PUT bots/{id}` | 编辑面板 bot，请求同上，三个字段都必填，返回改后的 bot；配置 bot 返回 403 `FORBIDDEN`，不存在返回 404，正在删除返回 409 `CHAT_BUSY` |
| `DELETE bots/{id}` | 删除面板 bot 和它的全部聊天，请求 `{"confirm_name"}`，返回 204；名字不一致返回 400，配置 bot 返回 403，不存在返回 404，正在删除或有聊天的任务没停下返回 409 `CHAT_BUSY`，可以重试。见 [面板 bot](#面板-bot) |
| `PUT conversations/{key}/policy` | 请求体同 API 的 `PolicyPut`，返回新的当前版本 |
| `POST conversations/{key}/policy/restore` | 请求 `{"version","reason"}`，把该历史版本的策略写成新版本 |
| `POST conversations/{key}/pause`、`POST conversations/{key}/resume` | 暂停、恢复 |
| `PUT conversations/{key}/persona` | 请求体同 API 的 `PersonaPut`（整份替换、回滚到版本、套用模板） |
| `PUT conversations/{key}/memory/members/{user_id}` | 改备注或好感度，规则同 API |
| `DELETE conversations/{key}/memory/members/{user_id}` | 忘记该成员，返回 204 |
| `POST conversations/{key}/memory/consolidate` | 触发一次整理，返回 202 |
| `PUT conversations/{key}/autonomy/buffs/{name}` | 请求 `{"value","reason"}`，把私聊的这条状态条设成指定值，规则同 API（不受模型调整的上限约束），变化记录的来源为 `panel`，返回 204；不是私聊、值不在 0–100、人设里没有这一条时返回 400 |
| `DELETE conversations/{key}/autonomy/status` | 结束私聊的当前状态并取消回来说话的定时，返回 204；不是私聊返回 400 |
| `POST conversations/{key}/messages` | 面板聊天发消息，请求 `{"text"}`，返回 202 `{"message_id"}`。只限私聊；群聊、文本为空或超过 2000 字、接管的会话还没有用户消息、bot 不回复这个发言人（私聊白名单不允许）时返回 400；bot 正在删除时返回 409 `CHAT_BUSY` |
| `POST chats` | 新建面板聊天，请求 `{"bot_instance_id","display_name"}`，返回 201 `{"key"}`；bot 不存在时返回 404，正在删除时返回 409 `CHAT_BUSY` |
| `DELETE conversations/{key}` | 删除面板聊天，返回 204；不是 `panel` 平台的会话返回 400；聊天里被取消的任务 5 秒内没停下时返回 409 `CHAT_BUSY`，可以重试 |
| `PUT persona-templates/{name}` | 请求 `{"persona","reason"}`（`persona` 必填，是整份人设，`reason` 只进审计），覆盖同名模板或新建模板；返回模板，格式同 `GET persona-templates/{name}`，新建时 201，否则 200。名字或内容不合规则、图片不存在返回 400 |
| `POST persona-templates/{name}/reset` | 只对 `default` 有效：删掉数据库里的 `default`，返回内置的默认人设；没有保存过时照样返回 200。其他名字返回 400 |
| `DELETE persona-templates/{name}` | 删除模板，返回 204；`default` 返回 403 `FORBIDDEN`，还有 bot 在用返回 409 `TEMPLATE_IN_USE`（`message` 列出这些 bot），不存在返回 404 |
| `POST persona-images` | 请求体是 PNG 或 JPEG 文件本身，最多 8 MiB，返回 201 `{"id","width","height","reply_tokens"}`；超过 8 MiB 返回 413，格式或尺寸不合格返回 400，正在处理另一张图片时返回 409 `CHAT_BUSY`。见 [人设模板和外观参考图](#人设模板和外观参考图) |
| `POST persona-images/{id}/describe` | 用识图模型生成外观介绍，返回 `{"text"}`，不保存；图片不存在返回 404，正在生成另一张时返回 409 `CHAT_BUSY`，没有配置 `vision` 返回 503、超时 504、模型调用失败 502（都是 `MODEL_UNAVAILABLE`） |

## 指标

面板和用量记录新增的 Prometheus 指标（`/metrics`）：

| 指标 | 标签 | 说明 |
|---|---|---|
| `conloom_usage_dropped_total` | — | 队列满或正在退出时丢弃的用量记录 |
| `conloom_usage_write_errors_total` | — | 用量记录写库失败的次数 |
| `conloom_panel_login_failures_total` | — | 面板登录失败（密码错或用户不存在）的次数 |
| `conloom_panel_requests_total` | `route`（路由模板，不含会话键）、`code` | 面板的请求数 |
