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
- **聊天**：和 bot 私聊，管理面板 bot，修改 QQ、Telegram bot 的设置。地址是 `#/chat`，打开某个聊天时是 `#/chat/<会话键>`（会话键经 URL 编码），刷新页面或打开这个链接都会回到同一个聊天。以前会话详情里“聊天”标签的链接 `#/conversations/<会话键>/chat` 会自动换成这个地址，旧书签照样能用。
  - 左栏按 bot 分组列出面板里新建的聊天，组的顺序同 `GET bots`（先配置 bot，再按新建顺序列面板 bot），组内最近活动的在前，每个 bot 最多 200 个。QQ、Telegram 上的私聊和群聊不列出，在“会话”里查看。每项是对方的名字和最后一条消息的时间（今天的显示时刻，更早的显示日期）。用法见下文 [在面板里聊天](#在面板里聊天)。
  - 组标题是 bot 的名字。配置文件里的 bot 标“配置”，标题旁有“编辑”，打开它的设置表单（见 [平台 bot 的设置](#平台-bot-的设置)），不能删除；面板 bot 的组标题旁有“编辑”“删除”（见 [面板 bot](#面板-bot)）。引用的模板或技能已经不在时在标题下面标出。每组下面有“新建聊天”，左栏最上面有“新建 Bot”。
  - 左栏在打开聊天页时读取一次，新建 bot、编辑、删除和新建聊天之后重新读取；切换聊天只换右栏，左栏不重新读取，也不定时刷新。要重新读取（比如别的面板用户新建了聊天）时，点左栏上方的“刷新”。
  - 右栏是选中的聊天：标题是对方的名字和 bot，“会话详情”链接到这个会话的页面。还没选聊天时显示提示。地址里是平台上的会话时，只提示“聊天页只能打开面板里新建的聊天”，并给出会话详情的链接。
  - 窄屏（手机）上左栏在上、右栏在下，打开一个聊天时页面滚到聊天处。
- **会话**：所有 bot 的全部群聊和私聊，带消息数、最后一条消息的时间、是否暂停、主动发言和记忆开关（面板里新建的聊天的记忆开关按最近有消息的那个面板聊天的策略，同记忆页）、人设模板和版本。私聊的主动发言徽章显示主动找人的档位，如“主动：中”；总开关或档位关闭时显示“主动关”。右上角的“新建聊天”在面板里和 bot 开一个私聊，建好后在聊天页打开，见下文 [在面板里聊天](#在面板里聊天)。点进一个会话默认打开“消息”标签；面板里新建的聊天的详情里有“在聊天页打开”和“删除这个聊天”。有以下标签页：
  - **消息**：按时间倒序浏览，从面板发出或送到面板的消息带“面板”标记（以前的版本能从面板接管平台私聊，那时发的消息留在平台私聊的记录里，同样带这个标记）；也可以输入关键词检索，规则和 `recall` 的关键词检索相同（任一词命中，按相关度排序，同分时新的在前），可以限定日期。
  - **策略**：常用字段做成了表单（主动发言、记忆、历史条数、分条上限 `reply.max_parts`，记忆的摘要和笔记字数上限 `memory.summary_max_chars`、`note_max_chars`（0 表示按会话类型的默认值，提示里写出本会话的默认值和当前生效的值）；私聊会话还有“私聊等待” `reply.private_wait` 和“私聊最长等待” `reply.private_max_wait`，写成 `6s`、`500ms`、`1m` 这样的时长，两项都填 0 表示用默认值；以及“主动找人档位” `proactive.private.level`（留空按“中”）和“免打扰开始”“免打扰结束” `proactive.private.quiet_start`、`quiet_end`，写成 `23:00` 这样的两位数时间，按 `server.timezone`，两项要么都填、要么都留空，都留空时按对方的作息推算，见 [配置：私聊主动找人](configuration.md#群策略)），另有“高级：JSON 编辑”可以整份替换；保存时服务端校验，不合法的值会在表单下方显示服务端给出的错误；暂停、恢复；历史版本列表，可以把任一版本恢复为新版本。字段含义见 [使用指南：私聊的节奏](usage.md#私聊的节奏)。
  - **人设**：编辑人设的各个字段、套用模板（人设模板目录里的，见 [人设模板和外观参考图](#人设模板和外观参考图)）、回滚到历史版本。会话还没有人设时只显示“尚未生成”和将要使用的模板，查看不会生成第 1 版。
    - “性别”下拉框对应 `gender`：不指定（空）、女（`female`）、男（`male`）。选了女或男时，系统提示在“角色设定”之后写一行“性别：女。”或“性别：男。”。
    - “内容尺度”一栏有“未成年角色”和“成年人设”两个开关，以及“内容规则”“记忆规则”两个文本框，对应人设的 `minor`、`adult`、`content_rules`、`memory_rules`。勾选“未成年角色”后，“成年人设”会被取消并且不可用；服务端同样拒绝两者同时为真。两段规则只在开放模式（成年、非未成年、私聊）下生效，规则见 [配置：内容模式](configuration.md#内容模式)。
    - 页面顶部显示“私聊内容模式”：当前人设（还没有人设时是将要使用的模板）放在私聊里会是开放还是保护模式。群聊不论人设如何都是保护模式。
    - “状态条”一栏编辑人设的 `bars`：每行是名称、基准、半衰期（写成 `6h`、`90m` 这样的时长）和说明，可以添加和删除，最多 4 行。一行都没有时保存的人设不带 `bars`，运行时用默认的心情和精力。限制见 [配置：状态条](configuration.md#状态条)。
    - “外观”一栏：“外观描述”对应 `appearance`（最多 800 字，不为空时作为“外貌”一段写进系统提示）。参考图显示 `appearance_image` 的缩小图，下面有三个按钮：“上传图片”（PNG 或 JPEG，选好后先在本地预览，同时上传到服务器，成功后显示服务器上的缩小图、原图尺寸和回复时约占的 token）；“生成外观介绍”（让识图模型看这张图，把结果填进外观描述框，框里已有内容时先确认，最多约 40 秒）；“移除图片”。“回复时附带外观图”默认勾选，取消勾选保存为 `appearance_image_skip: true`。上传、生成和移除都只改表单，点保存才写进人设。参考图读取失败（比如已被清理）时提示重新上传或移除：引用不存在的图片时服务端拒绝保存。
    - 人设模板页用同一个表单，见下面的“人设模板”。
  - **记忆**：摘要、整理进度和最近的错误，成员的备注和好感度，可以修改备注或好感度、忘记某个成员、立即整理一次，也可以查看某个成员（或摘要）的修订记录。这里的数据和 Bearer API 的 `GET /api/v1/conversations/{id}/memory` 完全相同：被忘记的成员没有备注和好感度，不会出现在列表里。
    - 面板里新建的聊天，这一页最上面写着“这个 bot 的所有面板聊天共用这份记忆”：显示和修改的是这个 bot 所有面板聊天共用的那份（会话的 `memory_scope` 是 bot 的共享范围键），从哪个面板聊天打开都一样；“记忆”是否生效、字数上限和“立即整理”能不能用，按最近有消息的那个面板聊天的策略。规则见 [使用指南：记忆与好感度](usage.md#记忆与好感度)。QQ、Telegram 上的私聊和群聊没有这行说明，各自一份。
    - 编辑成员时，备注框下方写出本会话当前生效的备注字数上限（`note_max_chars`），超过时服务端拒绝保存。
    - 编辑成员时，好感度输入框下方有“-5”“+5”“+10”“设为 100”四个快捷按钮：按已保存的好感度（还没有时从初始值 20 起算）计算新值，限制在 0–100，点击后立即保存，只改好感度；原因取“原因”框的内容，留空时记为“快捷调整 +5”这样的文字。备注或好感度框里有未保存的修改时先弹窗确认（这些修改会丢失），取消则不保存。保存过程中四个按钮都不可用。结果已经是当前值时只提示“好感度已经是 N”，不写入。
  - **自主**：只有私聊有。机器人在这个私聊里的状态、状态条、效果和主动找人的情况，数据同 [HTTP API：自主状态](api.md#自主状态)；写入后页面重新读取。面板里新建的聊天，这一页最上面同样写着“这个 bot 的所有面板聊天共用这份记忆”：当前状态、状态条、效果和最近变化是这个 bot 所有面板聊天共用的，在哪个面板聊天里看、改、删、结束都一样；主动找人和定时是这个聊天自己的，回来说话的定时只出现在设状态的那个聊天里。
    - 当前状态：在做什么、开始和预计结束的时间。“结束状态”按钮确认后立即结束它，同时取消回来说话的定时。
    - 状态条：先列人设的，再按添加的顺序列 bot 用 `add_buff` 自建的，自建的条名旁标“自建”。每条显示回落到现在的值（用进度条显示，按这一条自己的半衰期回落）、档位词和基准值。输入 0–100 的整数和可选的原因后保存，直接设成这个值，不受模型调整的单次和每日上限约束，变化记录的来源为 `panel`；自建的条也能这样改。自建的条有“删除”按钮，确认后删掉它的定义和数值；人设的条不能删。人设后来加了和自建条同名的条时，表里只列人设的那条，表下另起一行提示自建的那条已被代替、仍占一个自建名额，旁边的“删除”只删自建的那条，人设的那条保留现在的数值；同一个 bot 别的面板聊天的当前人设有同名的条时，删除也只删自建的定义，数值留给那条人设的条。
    - 效果：bot 用 `add_buff` 给自己加的、还在生效的效果，列出名字、说明、剩余时间（打开页面时算，例如“约 1 小时 20 分”）、开始和结束时间，“删除”按钮确认后提前去掉它。到时间的效果不列出。
    - 面板只能删除自建的状态条和效果，不能新增，新增交给 bot。
    - 最近变化：最近 50 次状态条和效果变化的时间、名称、类型（调整、新增状态条、删除状态条、新增效果、去掉效果）、变化（调整的前后值，新增状态条的初始值）、原因和来源（模型、面板或 API）。
    - 主动找人：总开关、档位（策略没设时注明按默认）、今天已发起几条和上限（“今天 1/2”）、免打扰时段及其来源（设定、按作息推算，或消息不够推算时的默认值）、对方再次发言前还能发起几条（发出的回来说话也占一条），以及还没触发的定时（回来说话、主动找人、追问）和时间。这几项在“策略”里修改。面板新建的聊天注明不会主动找人和追问。
    - 状态内容、变化原因，以及 bot 自建的状态条和效果的名字、说明，是模型或管理员写的文字，按纯文本显示。
  - **任务**：这个会话的任务。
- **人设模板**：数据库里的模板，以及没有保存过 `default` 时内置的默认人设，规则见 [人设模板和外观参考图](#人设模板和外观参考图)。地址是 `#/templates`，打开某个模板时是 `#/templates/<模板名>`。
  - 列表：模板名、人设名、角色名、有没有参考图、最后修改的人和时间，每行最后有“导出”。没有保存过的 `default` 在名字旁标“内置”，其他模板不标来源。模板和 bot 分开管理，列表不列哪些 bot 在用它，打开模板时可以看到。
  - **新建模板**：页头的“新建模板”打开弹窗，填两项：模板名（1–32 个小写字母、数字、`-` 或 `_`，不能是 `export` 或 `import`，不能和已有模板重名，不合规则时在弹窗里提示，不提交）；起点，“空白（内置默认人设）”或“复制 <模板名>”。空白取内置的默认人设（`GET persona-templates/default/builtin`，保存过 `default` 也一样），复制取那个模板保存过的版本，原模板不变。点“新建”后先读一次模板列表，名字已被占用就提示、不写入，再 `PUT persona-templates/{name}` 写入，然后打开新模板（两个人几乎同时用同一个名字新建时，后写的仍会覆盖先写的）。
  - **导出**：每个模板的“导出”和页头的“导出全部”是普通的下载链接（同源 `GET persona-templates/{name}/export` 和 `GET persona-templates/export`，带登录 cookie），浏览器按服务端给的文件名保存：`persona-<模板名>.json`、`persona-templates-<YYYYMMDD>.json`。页头注明导入的文件最大 64 MiB、最多 200 个模板：模板多、参考图大时，“导出全部”的文件可能超过这个上限、没法整个导入，这时逐个导出。
  - **导入**：页头的“导入”选一个导出的 JSON 文件，单个模板和全部模板的都可以；超过 64 MiB 的在浏览器里直接拒绝，不上传。文件先按 `on_conflict=fail` 提交：没有同名模板就直接导入；返回 409 `TEMPLATE_EXISTS` 时弹窗列出同名的模板（`error.names`），三个选项：“覆盖”（`overwrite`，用文件里的内容替换它们）、“改名导入”（`rename`，以 `名字-2` 这样没被占用的名字导入）、“取消”（什么都不导入）；文件里的其他模板两种方式下都照常导入。选了“覆盖”或“改名导入”、请求还没返回时，按 Esc 不会关掉弹窗；浏览器仍然把它关掉时，进度和之后的错误显示在页头。导入成功后列表重新读取，顶部列出结果：“新建”和“覆盖”了哪些模板，用的是最后的名字，改过名的注明文件里的名字，如“cat-2（文件里是 cat）”。其他错误（文件不合格的 400、超过上限的 413、正在处理另一张图片的 409 `CHAT_BUSY`）显示服务端的说明，再选一次文件即可重试。
  - 点进一个模板，用和会话“人设”页相同的表单编辑（包括外观描述、参考图和状态条），点“保存”写入。上方列出哪些 bot 把它当默认模板（“默认模板用于”，面板 bot 显示为“名字（面板）”）；没有保存过的 `default` 另注明来源“内置”。
  - `default` 有“恢复为内置默认”，没有保存过时不可用；其他模板有“删除”，任何时候都能删。确认提示里写明哪些 bot 把它当默认模板、删除后改用 `default`；删除后回到列表，顶部写明删掉了哪个模板、哪些 bot 改用了 `default`（响应的 `affected_bots`，写法和确认提示相同）。所有模板都有“导出”，下载的是保存过的版本，表单里没保存的修改不在里面。
  - 所有模板都有“复制为新模板”：输入新名字（规则同“新建模板”），用表单里当前的内容（包括还没保存的修改）新建一个面板模板，原模板不变，然后打开新模板。
  - 页面提示：改模板不影响已经在用它的会话（会话第一次回复时复制一份模板，之后各自独立）；要更新已有会话，到会话的“人设”页点“套用模板”。
- **任务**：全部任务，可以按状态、bot、会话筛选；任务详情里有 meta、生成的动作，以及这个任务的每次模型调用。任务受 `runtime.ops_retention`（默认 30 天）清理，更早的查不到。
- **审计**：审计记录，可以按种类和 bot 筛选，受 `runtime.audit_retention`（默认 90 天）清理。
- **配置**：当前生效的配置（环境变量展开以后），只读。见下文 [配置页打码](#配置页打码)。

所有用户数据（消息、昵称、人设、审计内容等）都按纯文本显示，里面的 HTML 不会被执行。

### 在面板里聊天

在“聊天”页可以直接和 bot 私聊：在聊天页某个 bot 那一组下点“新建聊天”（选好了这个 bot），或者在会话列表点“新建聊天”，选择 bot（面板 bot 显示为“名字（面板）”，配置 bot 显示为“名字（ID）”）、填写 bot 对你的称呼（留空时用面板用户名，最长 32 字），提交后在聊天页打开新聊天。新会话的平台是 `panel`，会话键形如 `qq-main:panel:private:panel_<随机>`，发言人 ID 是 `panel_<面板用户名>`。这个发言人是该 bot 的管理员，可以用管理员命令；私聊白名单和私聊开放范围对 `panel` 平台不生效。

面板只能在这样新建的聊天里发消息。QQ、Telegram 上的私聊和群聊不能从面板发消息（接口返回 400），在“会话”里查看。

- 消息和平台发来的消息走同一套流程：输入审查、限流、私聊的等待节奏都照常生效。消息长度 1 到 2000 字。
- 从面板发出的消息记为 `panel` 通道，带“面板”标记。bot 在面板聊天里发的消息（回复和回来说话）也走 `panel` 通道：只在面板里显示，适配器拉取发件箱时拿不到。
- 打开一个聊天时显示最近 50 条消息，之后每 1.5 秒拉一次新消息，切到别的聊天或离开聊天页就停止；聊天已经不存在（被删除，返回 404）时也停止，只显示一句“这个聊天已不存在”，不能再发消息。bot 自己的消息在左，用户的在右。会话有排队中或运行中的任务，或者面板通道还有没送达、也没过期的分条时，显示“对方正在输入…”。
- 回车发送，Shift+回车换行；输入法正在组字时按回车只是上屏，不发送。手机等以触屏为主的设备上回车换行，点“发送”发送。发送失败时显示服务端返回的错误，输入框里的内容保留。消息存下了但 bot 不会回复时（聊天暂停、发得太快、待回复的消息太多，即接口返回的 `suppressed_reason`），输入框下方说明原因。
- **建议回复**：输入框下方有“建议回复”开关，默认打开，状态存在浏览器本地（`localStorage`，换浏览器或清除网站数据后回到打开）。开关打开时，bot 一轮回复发完（不再显示“对方正在输入…”，最后一条消息是 bot 的）后，面板为这条消息请求一次建议（`POST conversations/{key}/suggestions`），在最后一条消息下面显示最多 3 个按钮，是替你写的下一句话（大致是追问、推进剧情、换话题各一句），点一下就直接发送这句话。在输入框里打字、发出消息或者有新消息时按钮消失；建议送到时最后一条消息已经变了、bot 又在输入或者你已经开始打字，就不显示。没有合格的建议或生成失败时什么都不显示。
  - 服务端用回复模型（`short_context`）生成，只看这个聊天最近 12 条消息（`/忘记我` 之前的不看，每条最多看 200 字）和 bot 的角色名，每句不超过 40 字。这次请求关闭思考：模型配置的 `extra_body` 里有 `chat_template_kwargs` 时把其中的 `enable_thinking` 改为 `false`，顶层有 `enable_thinking` 时也改为 `false`，配置本身不变；两者都没有时不加任何字段。`short_context` 熔断中时不生成。
  - 内容规则和回复相同：保护模式下提示里带同样的内容底线，每句经保护模式的输出审查；开放模式下经普通的输出审查。审查要拦下或改动的句子直接丢掉。
  - 同一条消息的建议缓存在内存里（最多 256 个聊天，最久没用的先丢，重启清空），再次请求不再调用模型；同一个聊天同时只生成一次，别的请求等它完成，是同一条消息的就共用结果。一次最多 20 秒，超时、失败或一句合格的都没有时返回空列表，空列表不缓存，下次请求再试。用量按用途 `suggest`（标签“建议回复”）记录；日志只记次数和 token，不记内容。
- 同一个 bot 的所有面板聊天共用一份记忆：摘要、对你的笔记和好感度、状态、状态条和效果；聊天记录、人设和策略是每个聊天自己的，回复时的近期消息只来自当前聊天。见 [使用指南：记忆与好感度](usage.md#记忆与好感度)。
- 有几个面板用户时，所有面板用户在同一个 bot 的面板聊天里共用这一份：笔记和好感度每人一份，摘要、状态、状态条和效果是大家共同的。任何一个面板用户的 `/忘记我`（或在“记忆”里忘记某个面板用户）都会清掉共同的那部分：共用的摘要、状态、状态条、效果、这个 bot 所有面板聊天的定时，以及所有面板聊天（包括别的用户新建的聊天）里忘记之前的语义检索数据，整理进度也跳到最新，别的用户还没整理的消息不再整理。别的用户的笔记、好感度和他们说话时回复的上下文不受影响。

面板里新建的聊天详情页有“删除这个聊天”按钮，确认后删除这个会话键下的全部数据（消息、任务、动作、人设、策略、定时，以及由这个聊天的消息生成的语义检索数据等），只保留用量统计（`model_calls`）和审计记录。这个 bot 的面板聊天共用的记忆保留，之后新建的面板聊天接着用；已经整理进摘要和笔记的内容不会因为删掉聊天而去掉，要清掉就用“忘记”。共用的状态是在这个聊天里设的时一起结束。平台上的会话不能在面板里删除。

- 删除前先取消这个聊天里排队和正在生成的任务，最多等 5 秒让它们停下；没停下时什么都不删，返回 409 `CHAT_BUSY`（`retryable: true`），稍后重试即可。
- 被 bot 忽略的消息不存正文、也没有任务，只留一条事件记录，删除时认不出它属于这个聊天；这些记录留到事件的保留期（`runtime.idempotency_retention`）满了由清理任务删除。
- 用量统计（`model_calls`）和审计记录按设计保留，分别到 `runtime.usage_retention`、`runtime.audit_retention` 到期清理。
- 删除时如果这个聊天的语义索引（`embedding`）正好在进行，它可能在删除之后再写入几行向量数据。这些行挂在已删除的会话键下，会话键不会再被使用（新建聊天总是用新的随机 ID），所以不会出现在任何会话里，只占少量空间。正好在进行的记忆整理写的是共用的记忆，照常写入。

### 面板 bot

面板 bot 是在面板里新建、只在面板里聊天的 bot：有自己的名字、默认人设模板、技能，每个聊天有自己的人设和策略，所有聊天共用一份记忆（见 [在面板里聊天](#在面板里聊天)）；不接任何平台，也不能发网关令牌。配置文件里的 bot（QQ、Telegram）的设置同样在面板里改，见 [平台 bot 的设置](#平台-bot-的设置)。用法见 [使用指南：面板 bot](usage.md#面板-bot)，设计见 `docs/superpowers/specs/2026-11-15-panel-bots-design.md`。

- **在哪里管理**：在“聊天”页。左栏最上面的“新建 Bot”打开表单：名字、默认人设模板（下拉框，选项是 `GET bots` 的 `templates`，新建时选 `default`）、技能（复选框，列出 `GET bots/skills` 的技能和说明，新建时全选）。名字和别的 bot（包括配置 bot）重名时，表单里提示一句，不阻止保存。面板 bot 组标题旁的“编辑”打开同一个表单；“删除”打开确认框，写明会删掉几个聊天，输入和 bot 一样的名字后“删除”按钮才能点。删除返回 409 时确认框里说明原因、可以再点一次重试，左栏重新读取（可能已经删掉了一部分聊天）。正在看的聊天属于被删的 bot 时，右栏回到提示；删除返回 409 时，正在看的聊天已经被删掉的话右栏也回到提示。
- **ID**：服务自动生成，`panel-` 加 8 位小写字母和数字，如 `panel-k3x9q2ma`；用过的 ID（包括已删除的 bot 的）不会再用。配置文件里的 bot ID 不能以 `panel-` 开头，所以两类 ID 不会冲突。
- **校验**：名字去掉首尾空白后 1–32 字，不能有控制字符、换行，以及零宽字符、方向控制符这类不可见的格式字符（Unicode 的 Cf 类），可以和别的 bot 重名；默认人设模板必须存在，不填时用 `default`；技能必须已加载、允许在私聊里用、用到的工具都已注册，可以一个都不选，重复的只算一次。最多 50 个面板 bot。不通过时返回 400，`message` 是中文原因，如“名字不能为空”。
- **生效**：新建后立即出现在所有列出 bot 的地方（新建聊天、概览、会话、用量等），不用重启。改名字和技能从下一次回复起生效；默认人设模板只影响之后新建的、以及 bot 还没回复过的聊天，已有聊天的人设在会话的“人设”页里改。
- **重启后**：面板 bot 存在数据库里，启动时载入。引用的人设模板已经不存在时，运行时改用 `default`；某个技能不存在或不能在私聊里用时，运行时忽略它。两种情况都记一条警告日志，接口的 `problems` 里给出说明（如“人设模板“x”已不存在，暂用 default”），不影响启动；编辑并保存一次后说明消失。
- **删除**：请求要带上 bot 的名字确认，不一致返回 400。删除时先把 bot 标为“删除中”，这期间新建聊天、发消息、编辑和再次删除都返回 409 `CHAT_BUSY`；然后逐个删除它的聊天，规则同上面的“删除这个聊天”。
  - 某个聊天里被取消的任务 5 秒内没停下时，删除停在这里，返回 409 `CHAT_BUSY`（`retryable: true`）：已删的聊天不恢复，bot 保留、不再是“删除中”，可以重试。
  - 聊天都删完后，删掉这个 bot 的面板聊天共用的记忆（摘要、笔记、好感度、修订记录、语义检索数据、状态、状态条、效果和变化记录），再删掉 bot 和它名下残留的用户、事件记录。`bot_instances` 里的行（存着名字）、模型用量和审计记录保留，用量页按 bot 分组时把它显示为“名字（已删除）”。
- **审计**：新建、编辑、删除分别写 `panel.bot.create`、`panel.bot.update`、`panel.bot.delete`，`actor` 为 `panel:<用户名>`，`bot_instance_id` 和目标都是这个 bot 的 ID。内容是保存的名字、默认人设模板和技能；删除时是名字和删掉的聊天数 `chats`。

### 平台 bot 的设置

配置文件里的 bot（QQ、Telegram）在配置文件里只写身份（`id`、`platform`、`platform_self_id`），其余设置存在数据库里，在面板里改（见 [配置参考：bots](configuration.md#bots)，设计见 `docs/superpowers/specs/2026-11-16-panel-settings-design.md` 第 3 节）。

- **在哪里改**：“聊天”页左栏，配置 bot 组标题旁的“编辑”。表单最上面列出 ID、平台和平台自身 ID，只显示、不能改，注明在配置文件里修改。下面是：
  - 显示名；名字和别的 bot 重名时提示一句，不阻止保存；
  - 管理员，每行一个平台用户 ID；
  - 私聊开放范围（下拉框）：关闭（`off`）、只限管理员（`admins`）、管理员和私聊白名单里的人（`allowlist`）、所有人（`all`）；
  - 私聊白名单，每行一个平台用户 ID；开放范围不是 `allowlist` 而白名单里有 ID 时，表单提示白名单现在不生效；
  - 启用的群，每行一个群 ID；
  - 私聊默认模板、群聊默认模板（下拉框，选项是 `GET bots` 的 `templates`）；
  - 技能（复选框，列出 `GET bots/skills` 的 `config_skills`，只用于群聊或只用于私聊的技能注明）。

  文本框里的空行和每行首尾的空白不算。表单里显示的是运行时生效的值：模板已经不存在、技能不能用时显示的是代替它的 `default`、去掉它以后的技能，保存后 `problems` 里的说明随之消失。
- **校验**：显示名的规则同面板 bot（去掉首尾空白后 1–32 字，不能有控制字符、换行和不可见的格式字符）；管理员、私聊白名单和群的每个 ID 不能为空，不能有空白、冒号（`:`，会话键的分隔符）和其他不可见字符，重复的只算一次；私聊开放范围必须是上面四档之一；两个模板必须存在于模板目录，不填时用 `default`；技能必须已加载、用到的工具都已注册，重复的只算一次。只用于群聊的技能也可以启用，私聊里不会用到它（技能按会话类型筛选，同配置 bot 原来的规则）。8 项都必填，ID、平台、平台自身 ID 和面板 bot 的 `persona_template` 等其他字段一律拒绝。不通过时返回 400，`message` 是中文原因，如“管理员：ID“200 01”不能包含空白、冒号或不可见字符”，什么都不改。
- **生效**：保存后立即写入数据库并换上新的 bot 目录，core 马上按新设置工作：启用的群、管理员和私聊开放范围从下一条消息起，显示名和技能从下一次回复起；默认模板只影响之后新建的会话，以及 bot 还没回复过的会话。`GET /api/v1/capabilities` 立即返回新的显示名、启用的会话（群在前，私聊白名单在后）和私聊开放范围；适配器每 5 分钟拉一次，所以适配器那边最多 5 分钟后跟上，不用重启。显示名同时写进 `bot_instances`，以后从配置文件去掉这个 bot 时，用量页显示的“名字（已删除）”就是它。
- **删除**：配置 bot 不能在面板里删除，`DELETE bots/{id}` 返回 403；要去掉它就从配置文件删掉再重启。
- **审计**：每次保存写一条 `panel.bot.update`，`actor` 为 `panel:<用户名>`，`bot_instance_id` 和目标都是这个 bot 的 ID。内容是改了哪些设置 `changed`（按接口里的字段名，如 `["name","groups"]`，和保存前库里的设置比，库里还没有时和默认值比；没改任何项时为空列表），以及保存的显示名 `name`、私聊开放范围 `private_chat`、两个模板 `private_template`、`group_template` 和技能 `skills`。管理员、私聊白名单和群只记在 `changed` 里，ID 不进审计。

### 人设模板和外观参考图

人设模板只存在数据库里，在面板里修改和新建，人设可以带一张外观参考图。页面见上文的“人设模板”和会话的“人设”标签页。规则见 [配置：人设模板](configuration.md#人设模板)，设计见 `docs/superpowers/specs/2026-11-15-persona-templates-appearance-design.md`。

- **模板目录**：数据库里的模板，来源 `source` 为 `db`；数据库里没有 `default` 时加上内置的默认人设，来源 `builtin`。旧配置文件里的 `persona_templates` 在升级后第一次启动时导入数据库（见 [配置：人设模板](configuration.md#人设模板)）。改动立即生效，不用重启，重启后还在。
- **修改和新建**：`PUT persona-templates/{name}` 写入整份人设，覆盖同名模板，或者新建模板，新名字只能是 1–32 个小写字母、数字、`-` 或 `_`，也不能是 `export` 或 `import`（这两个名字留给导出和导入的接口；以前已经存下的同名模板照常修改）。内容的校验和会话人设相同（字数上限，`minor` 和 `adult` 不能同时为真，`appearance_image` 必须是已上传的图片），不通过返回 400。
- **恢复和删除**：`default` 只能“恢复为内置默认”（`POST …/reset`，删掉数据库里的 `default`），不能删除，删除返回 403，因为它是兜底的模板。其他模板只能删除，恢复返回 400；模板和 bot 分开管理，还有 bot（配置 bot 或面板 bot）用它作默认模板时也照常删除，返回 200，`affected_bots` 列出这些 bot，格式和模板列表的 `users` 相同（如 `[{"id":"qq-main","name":"织语","source":"config"}]`）。这些 bot 随即改用 `default`，面板上标出“人设模板“x”已不存在，暂用 default”（见下面的“bot”）；给它们另选一个模板后说明消失。
- **已有会话不变**：会话第一次回复时把模板复制成自己的人设，之后各自独立。改模板只影响之后新建的会话；要更新已有会话，在会话的“人设”页套用模板。
- **bot**：模板写入、恢复、删除或导入后，配置 bot 和面板 bot 按新的目录重新生效：引用的模板删掉后改用 `default` 并标出“人设模板“x”已不存在，暂用 default”，之后新建的会话从 `default` 开始；已经复制了这个模板的会话不变。引用的模板重新出现时，这条说明随之消失。
- **导出**：`GET persona-templates/{name}/export` 下载一个模板，`GET persona-templates/export` 下载全部模板（没有保存过 `default` 时包括内置的那份）。响应是 JSON 文件（`Content-Disposition: attachment`），文件名分别是 `persona-<模板名>.json` 和 `persona-templates-<YYYYMMDD>.json`（`server.timezone` 的当地日期）；模板不存在返回 404。`HEAD` 只返回这些响应头（模板不存在时同样是 404），不生成文件，也不写审计。两种导出格式相同，只是 `templates` 里的项数不同：

  ```json
  {"format":"conloom.persona-template","version":1,"exported_at":"2026-11-15T04:00:00Z",
   "templates":[{"name":"night-owl","persona":{"persona_name":"…","appearance_image":"<图片 ID>"},
                 "appearance_image":{"mime":"image/png","data":"<原图 base64>"}}]}
  ```

  外观图以原图的标准 base64 嵌在文件里，没有参考图时省略 `appearance_image`；人设保留原来的图片 ID。文件里没有操作者和修改时间。模板引用的图片已经不存在时（从旧配置文件迁移来的模板可能这样），导出的人设去掉这个 ID，服务端记一条警告。
- **导入**：`POST persona-templates/import?on_conflict=` 的请求体就是导出的文件本身（不是表单），单个模板和全部模板的文件都可以，最多 64 MiB，超过返回 413。一个文件最多 200 个模板：解析到第 201 个就停下，返回 400“一个文件最多 200 个模板”，不再检查其中的模板。先整体检查，任何一项不合格就返回 400，一项都不写，说明里最多列出前 20 处问题，更多时最后注明“等 N 处”（N 是问题总数）：`format` 和 `version` 必须是上面的值；不能有多余的字段；模板名必须是 1–32 个小写字母、数字、`-` 或 `_`，不能是 `export` 或 `import`，同一个文件里不能重名；人设的校验和修改模板相同（字数上限，`minor` 和 `adult` 不能同时为真）；内嵌的图片必须是标准 base64，格式和尺寸的要求同上传图片（按字节判断类型，不看 `mime`）；没有内嵌图片的人设引用的图片必须已经上传过，或者内嵌在同一个文件的其他模板里。内嵌图片按上传处理，ID 按内容重算，写进人设的 `appearance_image`。
  - 同名：只有数据库里已存的模板算同名，内置的 `default` 不算（导入的 `default` 直接存进数据库，取代内置的那份）。`on_conflict` 决定怎么处理：`fail`（默认）返回 409 `TEMPLATE_EXISTS`，`error.names` 列出同名的模板，什么都不写；`overwrite` 覆盖同名模板；`rename` 改名导入，用 `名字-2`、`名字-3`……中第一个没被占用的（文件里的名字也算占用），超过 32 个字符时先截短名字。其他值返回 400。
  - 图片和模板在一个事务里写入。返回 `{"templates":[{"name","requested","created"}]}`，按文件里的顺序：`name` 是最后用的名字，`requested` 是文件里的名字，`created` 为 false 表示覆盖了已有模板。
- **上传图片**：`POST persona-images` 的请求体就是图片文件本身（不是表单），最多 8 MiB，超过返回 413。格式按文件开头的字节判断，不看 `Content-Type`，只接受 PNG 和 JPEG；宽和高都不能超过 8000 像素、总共不超过 2500 万像素，先读文件头检查尺寸、再解码，不合格返回 400，什么都不存。有透明部分的 PNG 铺在白底上；JPEG 的方向标记（EXIF orientation）不处理，按像素原样使用。服务器存原图，另存一份长边不超过 768 像素、质量 85 的 JPEG 给回复模型用，返回 201 `{"id","width","height","reply_tokens"}`：`id` 是原图内容的 SHA-256（同一张图再传一次得到同一个 ID），`reply_tokens` 是缩小图按 `ceil(宽/28) × ceil(高/28) + 2` 估算的 token。
- **取图**：`GET persona-images/{id}` 默认返回缩小图（`image/jpeg`），`?size=original` 返回原图（按上传时识别的类型）。
- **生成外观介绍**：`POST persona-images/{id}/describe` 把原图缩到长边不超过 1536 像素发给识图模型 `model_profiles.vision`（见 [配置：model_profiles](configuration.md#model_profiles)），返回 `{"text"}`，不自动保存，确认后写进人设的“外观描述”再保存。没有配置 `vision` 时返回 503 `MODEL_UNAVAILABLE`“未配置识图模型”（`retryable: false`，重试没有用，要改配置）；40 秒内没有完成返回 504（`retryable: true`）、模型调用失败返回 502，都是 `MODEL_UNAVAILABLE`，细节只写日志；图片不存在返回 404。用量按用途 `vision` 记录。
- **一次一张**：解码一张大图要几百 MB 内存，所以上传和生成外观介绍各自同时只处理一个请求，导入模板和上传共用一个名额。导入在读请求体之前就占上这个名额，直到导入结束（文件上传的时间也算在内），所以同时只有一个导入文件在内存里；被拒的导入不读请求体。另一个请求同时到达时直接返回 409 `CHAT_BUSY`（`retryable: true`）“正在处理另一张图片，请稍后”，不排队。
- **清理**：没有被任何模板或会话人设版本（包括历史版本）引用的图片，上传满 7 天后由每小时的维护任务删除。刚上传、还没保存进人设的图片有这 7 天的余地；同一张图再上传一次重新计时。
- **审计**：模板写入、恢复、删除分别写 `persona.template.put`、`persona.template.reset`、`persona.template.delete`，目标是模板名，`put` 的内容是写入后的来源 `source`、是否新建 `created` 和原因 `reason`（新建空白模板时是“从内置默认人设新建”，复制时是“复制自 <模板名>”），`delete` 的内容是改用 `default` 的 bot `affected_bots`。导出写 `persona.template.export`，目标是模板名（导出全部时为空），内容是导出的模板名 `templates`。导入成功时写 `persona.template.import`，目标为空，内容是冲突方式 `on_conflict`、新建的 `created` 和覆盖的 `overwritten`（都是最后用的名字），改过名时另有 `renamed`（文件里的名字对应最后用的名字）；人设内容和图片不进审计。上传写 `persona.image.upload`，目标是图片 ID，内容是类型、宽高、字节数和 `reply_tokens`。生成外观介绍成功时写 `persona.appearance.describe`，目标是图片 ID，内容只有生成文字的字数 `chars`。都不属于某个 bot（`bot_instance_id` 为空），图片内容和生成的文字都不进审计和日志。

### 面板上的修改

面板上的修改和 Bearer API、聊天命令走同一套代码，校验规则、字数上限、好感度范围完全一致，校验失败时原样显示服务端返回的错误。

- 每次修改都写一条审计记录，`actor` 为 `panel:<用户名>`，`bot_instance_id` 是会话所属的 bot。种类沿用 API 的 `policy.update`、`persona.update`、`memory.update`、`memory.forget`、`autonomy.buff`（修改私聊的状态条）、`autonomy.bar.remove`（删除 bot 自建的状态条，只记条名）、`autonomy.effect.remove`（去掉 bot 的效果，只记名字）、`autonomy.status.clear`（结束私聊的状态）；触发整理写 `memory.consolidate`（API 触发整理不写审计）。登录成功写 `panel.login`。
- 面板聊天的审计：发消息写 `panel.message`（内容只有消息 ID，不含正文），新建聊天写 `panel.chat.create`，删除聊天写 `panel.chat.delete`；目标都是会话键。
- 面板 bot 的新建、编辑、删除写 `panel.bot.create`、`panel.bot.update`、`panel.bot.delete`，目标是 bot 的 ID，见 [面板 bot](#面板-bot)；保存配置 bot 的设置也写 `panel.bot.update`，见 [平台 bot 的设置](#平台-bot-的设置)。
- 人设模板的写入、恢复、删除写 `persona.template.put`、`persona.template.reset`、`persona.template.delete`，目标是模板名；导出和导入写 `persona.template.export`、`persona.template.import`；上传参考图写 `persona.image.upload`，生成外观介绍写 `persona.appearance.describe`，目标是图片 ID；这几种的 `bot_instance_id` 为空，见 [人设模板和外观参考图](#人设模板和外观参考图)。
- 策略和人设的新版本里 `updated_by` 为 `panel:<用户名>`；成员记忆的修订记录、状态条和效果的变化记录来源为 `panel`。
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
- `bots` 先列配置文件里的 bot，按文件里写的显示，也就是只有 `id`、`platform`、`platform_self_id`（其余设置存在数据库里，见 `GET bots`）；再列面板 bot（`source` 为 `panel`），按生效的设置显示。
- 没有配置 `embedding` 段时，`embedding` 显示为 `null`。

## 模型用量

每次模型调用都在 `model_calls` 表里记一行，重启不丢，面板的用量页和概览从这里统计。

- **记哪些调用**：
  - `chat`：回复（包括带工具的回复里的每一轮请求，每轮一行），成功和失败都记；
  - `memory`：记忆整理；
  - `embed_index`：后台为语义检索生成向量；
  - `embed_query`：检索时把查询句转成向量；
  - `vision`：识图模型生成外观描述（标签"识图"），没有 bot 和会话；
  - `suggest`：面板聊天的建议回复（标签"建议回复"），有 bot 和会话，没有任务。
- **每行的内容**：时间、bot、会话、任务（记忆整理和后台生成向量没有任务时为空）、用途、角色（如 `short_context`，向量调用记 `embedding`）、模型名（上游返回的；失败拿不到时用配置里的）、提示 / 回复 / 思考 token（上游没返回就记 0）、发请求前估算的输入 token（只有 `chat` 和 `suggest` 有）、耗时、结果和上游的结束原因（`finish_reason`）。结果是 `ok`、`error`、`timeout`、`canceled` 之一，`ok` 以外的都算失败。
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

- 错误格式和 Bearer API 相同：`{"error":{"code","message","retryable","trace_id"}}`；导入模板遇到同名时的 409 `TEMPLATE_EXISTS` 另有 `names`，列出同名的模板。没登录或会话已失效返回 401 `UNAUTHORIZED`，CSRF 或 `Origin` 检查不通过返回 403 `FORBIDDEN`，登录被限流返回 429 `RATE_LIMITED`。
- 时间是 RFC3339 UTC 字符串，可以为空的时间为 `null`。用量接口的 `from`、`to` 是 `server.timezone` 的当地日期 `YYYY-MM-DD`。
- 路径里的 `{key}` 是会话键 `bot:平台:类型:会话ID`（如 `qq-main:qq:group:group_10001`）经 URL 编码后的形式。面板跨 bot，所以不用 Bearer API 里那种按令牌绑定 bot 的会话 ID。
- 任务列表、审计记录、调用明细（`usage/calls`）和消息浏览用 `before` 游标加 `limit` 翻页；响应里的 `next_before` 是下一页的 `before`，最后一页为 `null`。消息检索改用 `offset` 翻页（见下表）。`limit` 都是默认 50、最大 200。会话列表不分页，一次返回全部会话。

**鉴权**

| 接口 | 说明 |
|---|---|
| `POST login` | 请求 `{"name","password"}`，`name` 去掉首尾空白后再比对（区分大小写），成功返回 `{"user","csrf_token","expires_at"}` 并设置 cookie |
| `GET session` | 当前用户、`csrf_token`、过期时间 |
| `POST logout` | 删除会话、清掉 cookie，返回 204 |

**读接口**

| 接口 | 说明 |
|---|---|
| `GET overview` | 版本、提交、启动时间、时区；各 bot（带 `source`，见 `GET bots`）及其 `last_seen_at`；实时状态 `live`；今天的用量 `today` |
| `GET usage?from=&to=&group=` | 每日分桶和区间合计；`group` 为 `model`（默认）、`role`、`purpose`、`bot`；不传日期时为截至今天的 7 天。`bots` 按 bot ID 给出每个 bot 的 `name` 和 `deleted`：bot 目录里的 bot 用当前的名字、`deleted` 为 false；只在 `bot_instances` 里登记过的（删除的面板 bot、从配置文件去掉的 bot）用登记的名字、`deleted` 为 true |
| `GET usage/calls?from=&to=&model=&purpose=&conversation=&before=&limit=` | 调用明细，新的在前；`conversation` 是会话键 |
| `GET metrics/live` | 本进程 Prometheus 指标的快照：计数器和仪表给值，直方图给次数、总和和按桶估算的 p50 / p95 |
| `GET bots` | 全部 bot `bots`：先配置 bot，再按新建顺序列面板 bot。每项 `id`、`name`、`source`（`config` 或 `panel`）、`platform`（面板 bot 为 `panel`）、`private_template` 和 `group_template`（新私聊、新群的默认模板，面板 bot 两项相同）、`persona_template`（同 `private_template`，即面板 bot 唯一的模板）和 `skills`（这几项都是运行时生效的值）、`problems`（失效引用的说明，如配置 bot 或面板 bot 的模板已不存在，没有时为空列表）、`chats`（私聊数）、`last_message_at`（这些私聊最后一条消息的时间）。配置 bot 另有配置文件里的 `platform_self_id`，以及运行时生效的 `admins`、`private_chat`、`private_allowlist`、`groups`（列表没有时为空列表），面板 bot 没有这几项。另有 `templates`：可选的人设模板名，按名字排序 |
| `GET bots/skills` | 面板 bot 可以启用的技能 `skills`（已加载、允许在私聊里用、工具齐全）和配置 bot 可以启用的技能 `config_skills`（已加载、工具齐全，也包括只用于群聊的）。每项 `name`、`description`、`conversations`（适用的会话类型 `private`、`group`），按名字排序 |
| `GET chats` | 聊天页左栏用：`bots` 按 bot 分组，顺序同 `GET bots`，每组 `id`、`name`、`source` 和这个 bot 在面板里新建的聊天 `chats`（没有时为空列表；平台上的私聊和群聊不列出）。聊天按最近活动从新到旧：取最后一条消息的时间和会话建立的时间中较晚的一个，所以刚新建、还没有消息的聊天排在最前；排好序后每个 bot 取前 200 个。每项 `key`、`title`（对方的显示名：面板用户在聊天里最新的昵称；还没人发言时是新建时填的称呼；都没有时是会话 ID）、`platform`（总是 `panel`）、`last_message_at` |
| `GET conversations` | 全部会话 `conversations`，以及全部 bot `bots`（`id`、`name`、`platform`、`source`，“新建聊天”从这里选；面板 bot 的 `platform` 和 `source` 都是 `panel`）。私聊会话的 `proactive_level` 是生效的主动找人档位（`off`、`low`、`mid`、`high`，没设时为 `mid`），其他会话为空字符串；`memory_scope` 是会话的记忆范围键：面板里新建的聊天是 bot 的共享范围 `<bot>:panel:private:~shared`，其他会话是它自己的会话键。单个会话的接口同样有这两个字段 |
| `GET conversations/{key}` | 单个会话 |
| `GET conversations/{key}/messages?before=&limit=` | 浏览消息，按 `seq` 游标翻页。每条消息带 `channel`：`panel` 表示从面板发出或送到面板，平台上的消息为空字符串 |
| `GET conversations/{key}/messages?q=&from=&to=&offset=&limit=` | 检索消息，按 `offset` 翻页，响应里的 `next_offset` 是下一页 |
| `GET conversations/{key}/chat?after=` | 面板聊天的增量拉取：`seq` 大于 `after`（默认 0）的消息，从旧到新，每次最多 200 条，格式同消息浏览；`typing` 为 true 表示会话有排队中或运行中的任务，或面板通道还有没送达、也没过期的分条；`next_after` 是下一次的 `after`（没有新消息时等于本次的 `after`） |
| `GET conversations/{key}/policy` | 当前策略、历史版本和 `memory_limits`（`summary_max_chars`、`note_max_chars` 是当前生效的记忆字数上限，面板里新建的聊天按最近有消息的那个面板聊天的策略，同记忆页；`default_summary_max_chars`、`default_note_max_chars` 是本会话类型的默认值，策略里写 0 时用它们） |
| `GET conversations/{key}/persona` | 当前人设、历史版本、可用模板；还没有人设时给出将要使用的模板，不写库。`minor` 是当前人设（没有时是该模板）的 `minor`，`content_mode` 是它在私聊里的内容模式（`open` 或 `protected`），与会话本身是群聊还是私聊无关 |
| `GET conversations/{key}/memory` | 与 `GET /api/v1/conversations/{id}/memory` 相同；面板里新建的聊天返回 bot 共享范围的数据 |
| `GET conversations/{key}/memory/revisions?user_id=` | 某成员的修订记录，不传 `user_id` 时是摘要的；按会话的记忆范围 |
| `GET conversations/{key}/autonomy` | 私聊的状态、状态条（bot 自建的 `custom` 为 true）、被人设代替的自建条（`shadowed_bars`）、还在生效的效果、最近的变化（带 `kind`）和主动找人的情况，与 `GET /api/v1/conversations/{id}/autonomy` 相同（见 [HTTP API：自主状态](api.md#自主状态)）；面板里新建的聊天的状态、状态条、效果和变化是 bot 共享范围的，主动找人和定时是这个聊天的；不是私聊返回 400 |
| `GET persona-templates` | 人设模板目录 `templates`，按名字排序。每项 `name`、`source`（`db` 或 `builtin`）、`persona_name`、`character_name`、`appearance_image`（没有参考图时为空字符串）、`users`（运行时用它作默认模板的 bot，每项 `id`、`name`、`source`，没有时为空列表）、`updated_by` 和 `updated_at`（最后写入的人和时间，内置的 `default` 为空字符串和 `null`） |
| `GET persona-templates/{name}` | 单个模板：列表里的字段，加上整份人设 `persona`；不存在返回 404 |
| `GET persona-templates/default/builtin` | 内置的默认人设 `{"persona"}`，保存过 `default` 时也是内置的那份；新建空白模板时从它开始 |
| `GET persona-templates/{name}/export` | 下载这个模板的导出文件 `persona-<模板名>.json`，不存在返回 404；写审计 `persona.template.export`（`HEAD` 只返回响应头，不写审计）。见 [人设模板和外观参考图](#人设模板和外观参考图) |
| `GET persona-templates/export` | 下载全部模板的导出文件 `persona-templates-<YYYYMMDD>.json`（`server.timezone` 的当地日期）；写审计 `persona.template.export`（`HEAD` 只返回响应头，不写审计）。文件一个模板一个模板地写出，同一时间只有一张参考图在内存里；已经开始写之后读取某个模板失败时，连接直接断开，下载失败，不会留下半截文件 |
| `GET persona-images/{id}?size=` | 图片本身：`size` 为 `reply`（默认，缩小图，`image/jpeg`）或 `original`（原图，`image/png` 或 `image/jpeg`）；其他值返回 400，图片不存在返回 404 |
| `GET tasks?status=&bot=&conversation=&before=&limit=` | 任务列表 |
| `GET tasks/{id}` | 任务详情：任务、meta、动作、模型调用 |
| `GET audit?kind=&bot=&before=&limit=` | 审计记录 |
| `GET config` | 打码后的当前配置 |

**写接口**（都要带 `X-CSRF-Token`，除了建议回复都写审计）

| 接口 | 说明 |
|---|---|
| `POST bots` | 新建面板 bot，请求 `{"name","persona_template","skills"}`（`persona_template` 省略时为 `default`，`skills` 省略时不启用技能），返回 201 和新 bot，格式同 `GET bots` 的一项；校验不通过返回 400 |
| `PUT bots/{id}` | 编辑面板 bot，请求同上，三个字段都必填，返回改后的 bot，格式同 `GET bots` 的一项；不存在返回 404，正在删除返回 409 `CHAT_BUSY`。`{id}` 是配置 bot 时改它的设置，请求 `{"name","admins","private_chat","private_allowlist","groups","private_template","group_template","skills"}`，8 项都必填，其他字段（包括 `id`、`platform`、`platform_self_id`、`persona_template`）返回 400；校验不通过返回 400，见 [平台 bot 的设置](#平台-bot-的设置) |
| `DELETE bots/{id}` | 删除面板 bot 和它的全部聊天，请求 `{"confirm_name"}`，返回 204；名字不一致返回 400，配置 bot 返回 403，不存在返回 404，正在删除或有聊天的任务没停下返回 409 `CHAT_BUSY`，可以重试。见 [面板 bot](#面板-bot) |
| `PUT conversations/{key}/policy` | 请求体同 API 的 `PolicyPut`，返回新的当前版本 |
| `POST conversations/{key}/policy/restore` | 请求 `{"version","reason"}`，把该历史版本的策略写成新版本；该版本在主动发言（`proactive`）或记忆（`memory`）一段出现之前保存、没有这一段时，保留当前的这一段 |
| `POST conversations/{key}/pause`、`POST conversations/{key}/resume` | 暂停、恢复 |
| `PUT conversations/{key}/persona` | 请求体同 API 的 `PersonaPut`（整份替换、回滚到版本、套用模板） |
| `PUT conversations/{key}/memory/members/{user_id}` | 改备注或好感度，规则同 API |
| `DELETE conversations/{key}/memory/members/{user_id}` | 忘记该成员，返回 204 |
| `POST conversations/{key}/memory/consolidate` | 触发一次整理，返回 202 |
| `PUT conversations/{key}/autonomy/buffs/{name}` | 请求 `{"value","reason"}`，把私聊的这条状态条设成指定值，规则同 API（不受模型调整的上限约束），变化记录的来源为 `panel`，返回 204；不是私聊、值不在 0–100、没有这一条（人设的或 bot 自建的）时返回 400 |
| `DELETE conversations/{key}/autonomy/bars/{name}` | 删除 bot 自建的这条状态条，规则同 API（人设后来加了同名条时只删自建的那条，人设的保留数值），变化记录的来源为 `panel`，审计 `autonomy.bar.remove`，返回 204；不是私聊、删人设的状态条时返回 400，没有这条自建的状态条时返回 404 |
| `DELETE conversations/{key}/autonomy/effects/{name}` | 提前去掉 bot 的这个效果，变化记录的来源为 `panel`，审计 `autonomy.effect.remove`，返回 204；不是私聊返回 400，没有这个还在生效的效果时返回 404 |
| `DELETE conversations/{key}/autonomy/status` | 结束私聊的当前状态并取消回来说话的定时，返回 204；不是私聊返回 400 |
| `POST conversations/{key}/messages` | 面板聊天发消息，请求 `{"text"}`，返回 202 `{"message_id"}`；消息存下了但 bot 不会回复时（如聊天暂停、限流、队列满）另带 `suppressed_reason`，取值同 API 的 `POST /events`（`paused`、`rate_limited`、`queue_full` 等）。只限 `panel` 平台的会话；平台上的会话（私聊和群聊）、文本为空或超过 2000 字时返回 400，什么都不存；bot 正在删除时返回 409 `CHAT_BUSY` |
| `POST conversations/{key}/suggestions` | 面板聊天的建议回复，请求 `{"after_seq"}`（聊天最后一条消息的 `seq`，正整数），返回 200 `{"suggestions":[…]}`，最多 3 句，没有时为空列表：`after_seq` 不是最后一条消息、最后一条不是 bot 的、生成失败或超时都返回空列表。只限 `panel` 平台的会话，其他会话返回 400，`after_seq` 缺少或不是正整数返回 400。什么都不改，不写审计。见 [在面板里聊天](#在面板里聊天) |
| `POST chats` | 新建面板聊天，请求 `{"bot_instance_id","display_name"}`，返回 201 `{"key"}`；bot 不存在时返回 404，正在删除时返回 409 `CHAT_BUSY` |
| `DELETE conversations/{key}` | 删除面板聊天，返回 204；不是 `panel` 平台的会话返回 400；聊天里被取消的任务 5 秒内没停下时返回 409 `CHAT_BUSY`，可以重试 |
| `PUT persona-templates/{name}` | 请求 `{"persona","reason"}`（`persona` 必填，是整份人设，`reason` 只进审计），覆盖同名模板或新建模板；返回模板，格式同 `GET persona-templates/{name}`，新建时 201，否则 200。名字或内容不合规则、图片不存在返回 400 |
| `POST persona-templates/{name}/reset` | 只对 `default` 有效：删掉数据库里的 `default`，返回内置的默认人设；没有保存过时照样返回 200。其他名字返回 400 |
| `DELETE persona-templates/{name}` | 删除模板，还有 bot 在用也照常删除，返回 200 `{"name","affected_bots"}`：`affected_bots` 是把它当默认模板的 bot，每项 `id`、`name`、`source`（同 `users`，没有时为空列表），它们随即改用 `default` 并标出问题；`default` 返回 403 `FORBIDDEN`，不存在返回 404 |
| `POST persona-templates/import?on_conflict=` | 请求体是导出的文件本身，最多 64 MiB；`on_conflict` 为 `fail`（默认）、`overwrite` 或 `rename`。返回 `{"templates":[{"name","requested","created"}]}`；超过 64 MiB 返回 413，超过 200 个模板或文件有任何问题返回 400、一项都不写，`fail` 时有同名模板返回 409 `TEMPLATE_EXISTS`（`names` 列出它们），正在处理另一张图片时返回 409 `CHAT_BUSY`。见 [人设模板和外观参考图](#人设模板和外观参考图) |
| `POST persona-images` | 请求体是 PNG 或 JPEG 文件本身，最多 8 MiB，返回 201 `{"id","width","height","reply_tokens"}`；超过 8 MiB 返回 413，格式或尺寸不合格返回 400，正在处理另一张图片时返回 409 `CHAT_BUSY`。见 [人设模板和外观参考图](#人设模板和外观参考图) |
| `POST persona-images/{id}/describe` | 用识图模型生成外观介绍，返回 `{"text"}`，不保存；图片不存在返回 404，正在生成另一张时返回 409 `CHAT_BUSY`，没有配置 `vision` 返回 503、超时 504、模型调用失败 502（都是 `MODEL_UNAVAILABLE`） |

记忆（`memory/…`）和自主状态（`autonomy/…`）的读写接口作用于会话的记忆范围：面板里新建的聊天读写的是 bot 所有面板聊天共用的那份，开关、字数上限和能不能整理按最近有消息的那个面板聊天的策略，规则同 [HTTP API：记忆](api.md#记忆)。

## 指标

面板和用量记录新增的 Prometheus 指标（`/metrics`）：

| 指标 | 标签 | 说明 |
|---|---|---|
| `conloom_usage_dropped_total` | — | 队列满或正在退出时丢弃的用量记录 |
| `conloom_usage_write_errors_total` | — | 用量记录写库失败的次数 |
| `conloom_panel_login_failures_total` | — | 面板登录失败（密码错或用户不存在）的次数 |
| `conloom_panel_requests_total` | `route`（路由模板，不含会话键）、`code` | 面板的请求数 |
