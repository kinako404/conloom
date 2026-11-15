# 人设模板在线编辑与外观参考设计

日期：2026-11-15
状态：已定稿，待实施。部署者已确认：图挂在人设模板上，模板支持在线编辑，识图用 `grok-4.20-0309-reasoning`，回复模型也读这张图。标"实施者决定"的条目是我定的，部署者可以事后改。

## 1. 为什么做

- 部署者想给角色一张外观参考图（比如立绘），让角色描述自己外貌时前后一致。
- 图要能在面板里在线上传，然后一键让识图模型生成一段外观介绍，部署者再修改。
- 回复模型（qwen3.8）支持读图，回复时也能看到这张图。
- 现在人设模板只能写在配置文件里，改一次要重启；所以先把模板搬到线上，可以在面板里编辑。

## 2. 目标、非目标、反目标

**目标**

1. **模板目录**：配置文件里的模板，加上在面板里修改或新建的模板，合成一份目录。
   - 面板里修改配置模板，算"在线覆盖"，随时可以"恢复为配置文件版本"。
   - 面板里也可以新建模板，或者复制已有模板再改。
   - 改动立即生效，不用重启；重启后在线改动都还在。
2. **人设新增两项**：
   - `appearance`：外观描述，文字。
   - `appearance_image`：外观参考图的引用。
   - 任何人设都能用这两项，包括模板和每个会话自己的人设。
3. **上传参考图**：在模板编辑页和会话的人设页都能上传 PNG 或 JPEG。服务器保存原图，同时生成一份给回复模型用的缩小图。
4. **一键生成外观介绍**：用新的可选模型配置 `vision`（`grok-4.20-0309-reasoning`）识图，生成的文字填进外观描述框，部署者确认后再保存。
5. **回复时附带图片**：回复时，如果会话人设里有参考图、所选模型声明支持图片、人设没关掉"回复时附带外观图"，就把缩小图放进请求。
6. 记审计：`persona.template.put`、`persona.template.reset`、`persona.template.delete`、`persona.image.upload`、`persona.appearance.describe`。

**非目标**

- 处理群友在聊天里发的图片（现在仍然只显示"[图片]"）。
- 在面板里修改配置文件本身。
- 模板改了以后，自动更新已经在用这个模板的会话。现在的规则是：会话第一次回复时把模板复制一份，之后各自独立。要更新已有会话，在会话的人设页点"套用模板"。
- 支持 WebP、GIF 等其他格式。

**反目标**（技术上能跑但算失败）

- 图片原文件或缩小图进入公开仓库。图只存在服务器数据库里。
- 没有图片的人设，系统提示和回复请求和现在逐字节不同。
- 所选模型不支持图片时，请求里还带着图片，导致报错。
- 图片让回复的 token 估算偏低，导致上下文超限。
- 模板在线修改绕过内容规则：`minor` 和 `adult` 不能同时为真，这条校验照旧，保护模式照旧。
- 在线编辑模板时，正在进行的回复读到半新半旧的模板。

## 3. 考虑过的做法

- **图挂在模板上，模板搬到线上（部署者选定）**：新会话和面板 bot 用这个模板就自动带图。
- **图只挂在每个会话的人设上**：改动小，但每个会话都要单独上传一次。本设计仍然允许会话人设有自己的图，只是主要入口在模板上。
- **模板改写配置文件**：服务没有配置文件的写权限，注释会丢失，还要重启。放弃。

## 4. 设计

### 4.1 人设字段

在 `PersonaDoc` 里增加三个字段，零值时序列化结果和现在一样：

```go
Appearance          string `json:"appearance,omitempty" yaml:"appearance,omitempty"`                       // 外观描述，最多 800 字
AppearanceImage     string `json:"appearance_image,omitempty" yaml:"appearance_image,omitempty"`           // 图片 ID（sha256 十六进制）
AppearanceImageSkip bool   `json:"appearance_image_skip,omitempty" yaml:"appearance_image_skip,omitempty"` // 回复时不附带外观图
```

- `Render`：外观描述不为空时，在"角色设定"之后、"说话方式"之前加一段：

  ```
  外貌：
  <描述>
  ```

  外观描述为空时，输出和现在逐字节相同，现有的旧人设渲染测试必须照样通过。
- 图片 ID 不进系统提示。
- 写入人设时（模板、会话人设、API），`AppearanceImage` 不为空就必须是已存在的图片 ID，否则返回 400。
- `DefaultPersona()` 不变。

### 4.2 图片存储

迁移 `0014_persona_templates.sql` 建两张表（`0013` 已留给面板 bot）：

```sql
CREATE TABLE persona_images (
  id TEXT PRIMARY KEY,            -- 原图字节的 sha256 十六进制
  mime TEXT NOT NULL,             -- image/png | image/jpeg
  width INTEGER NOT NULL, height INTEGER NOT NULL,
  original BLOB NOT NULL,
  reply_jpeg BLOB NOT NULL,       -- 缩小图，长边 ≤ 768 px，JPEG 质量 85
  reply_width INTEGER NOT NULL, reply_height INTEGER NOT NULL,
  reply_tokens INTEGER NOT NULL,  -- 缩小图的 token 估算
  created_by TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE persona_templates (
  name TEXT PRIMARY KEY,
  persona TEXT NOT NULL,          -- PersonaDoc JSON
  updated_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
```

- **图片只增不改**：ID 是原图的哈希，同一张图重复上传得到同一个 ID。会话人设版本里存的是 ID，图片永远不会被改写。
- **清理**：没有任何模板或人设版本引用的图片，创建满 7 天后由每小时的维护任务删除。新上传、还没保存进人设的图片留有这 7 天的余地。（实施者决定）
- **上传处理**，只用标准库：
  - 格式按文件开头的字节判断（`http.DetectContentType`），不信客户端声明的类型，只接受 PNG 和 JPEG。
  - 请求体最多 8 MiB。
  - 解码前先用 `DecodeConfig` 检查尺寸：宽高都不超过 8000，总像素不超过 2500 万。
  - 有透明通道的 PNG 先铺到白色背景上。
  - 用自己写的区域平均缩小算法，把长边缩到不超过 768 像素，编码成质量 85 的 JPEG。
  - JPEG 的方向标记（EXIF orientation）不处理，在文档里说明。
- **token 估算**：`ceil(宽/28) × ceil(高/28) + 2`。比 Qwen 实际用的 32 像素一块更保守。
- **备份**：`VACUUM INTO` 会自动带上新表，文档里说明数据库会因为图片变大。

### 4.3 模板目录

- `persona.Service` 持有一份合并后的模板目录。目录用原子指针保存，每次修改都生成一份新的完整 map 再替换，读的一方永远拿到完整的旧版或新版。
- 合并顺序：配置模板，然后数据库里的模板覆盖同名项、补上新名字，最后如果还没有 `default`，补上 `DefaultPersona()`。
- 写操作：
  - `PutTemplate(name, doc, actor)`：先写数据库，再在同一把锁内重建目录。
  - `ResetTemplate(name)`：删掉数据库里的覆盖。只对配置模板有效。
  - `DeleteTemplate(name)`：只能删面板新建的模板。
  - `default` 可以覆盖、可以恢复，但不能删除。
- 读操作：`Template(name)`、`TemplateNames()`、`Templates()`（列表，带来源：配置、配置·已在线修改、面板）。
- 现有直接读 `cfg.PersonaTemplates` 的地方都改用目录：`core/autonomy_service.go`、`panel/readviews.go`；`panel/configview.go` 继续只显示配置文件原样。
- 校验：
  - 模板名 1–32 个字符，只能用小写字母、数字、`-` 和 `_`。
  - 内容和现在一样，用 `PersonaDoc.Validate` 加 `ValidateContent` 检查，再检查图片 ID 存在。
  - 启动加载时，数据库里不合法的行记警告并跳过，不阻止启动。
- 删除面板模板时，如果还有面板 bot 引用它，返回 409，并列出这些 bot。
- 配置 bot 仍然只在配置校验时检查模板名是否存在于配置文件里；运行时取的是目录里的同名模板，可能是在线覆盖后的版本。

### 4.4 识图模型

- `model_profiles.vision`：可选的模型配置，结构和其他配置相同。
  - 校验时，如果有这个配置，它的 `modalities` 必须包含 `image`。
  - 路由选回复模型时永远不会选它。
  - 估算 token 用的系数取各配置最大值时，不算它。
- 线上配置：模型 `grok-4.20-0309-reasoning`，地址和密钥沿用现有网关的环境变量，`modalities: [text, image]`。
- **生成外观介绍**：`POST /panel/api/persona-images/{id}/describe`
  - 把原图缩到长边不超过 1536 像素，编码成 JPEG 发给 `vision`。
  - 用固定的中文提示，要求写一段 150–400 字、可以直接放进人设"外貌"部分的描述，只写看得见的外观：发型发色、眼睛、体型身高给人的印象、服装、配饰、标志性特征、整体气质。
  - 不写图里的文字、画师信息和背景。
  - 用第三人称，不加标题和列表。
  - 超时 40 秒。
  - 结果原样返回给面板，不自动保存，部署者确认后才写进人设。
  - 申请 `vision` 的并发名额；用量按用途 `vision` 记进 `model_calls`；没有配置 `vision` 时返回 503，并提示"未配置识图模型"。
- 推理模型的思考内容不返回，只取最终回答。

### 4.5 回复时附带图片

- 请求里的消息支持多段内容：`ChatMessage` 增加 `Images []string`（data URL）。为空时 JSON 和现在逐字节相同；不为空时 `content` 写成数组，先放图片，再放文字。
- `contextbuilder.Input` 增加 `Image *ImageRef{DataURL string; Tokens int}`。
  - `Build` 把它放在第一条 user 消息的最前面，文字部分开头加一行固定标签：`【你的外观参考图（这就是你自己的样子，不是对方发来的图片）】`。
  - `BaseTokens` 和估算 token 都计入图片的 token 和这行标签。
- runner 在取人设的同时，按会话人设的 `AppearanceImage` 读出缩小图，前提是人设没有设 `AppearanceImageSkip`。记忆整理不经过 runner，不带图。
- 真正发请求时，如果所选模型的 `modalities` 不含 `image`，就去掉图片再发。重新路由到其他模型时也按这条判断。
- token 估算一律按带图计算，宁可估多。
- 不设置路由的"需要图片"标记，以免把不支持图片的长上下文模型从备选里排除。
- 读图片出错时记警告，照常回复、不带图。
- 群聊和私聊都适用：这是 bot 自己的外观，不涉及用户。
- 图片在第一条 user 消息里，系统提示不变，系统提示缓存不受影响。

### 4.6 面板

- **顶部导航新增"人设模板"页**：
  - 列表：模板名、角色名、来源（配置／配置·已在线修改／面板）、有没有参考图、被哪些 bot 当作默认模板。
  - 编辑：复用会话人设页的人设表单。为此把表单抽成一个共用组件，两个页面都用。表单新增：
    - "外观描述"多行输入框；
    - 参考图区域：缩略图、"上传图片"按钮、"生成外观介绍"按钮，生成结果填进外观描述框；
    - "回复时附带外观图"复选框，默认勾选。
  - 按钮："保存"；配置模板有"恢复为配置文件版本"；面板模板有"删除"；所有模板都有"复制为新模板"。
  - 页面提示：改模板不影响已在用的会话，要更新请到会话人设页点"套用模板"。
- **会话人设页**：使用同一个表单组件，所以也能上传图、生成介绍。
- **上传**：浏览器把文件原样作为请求体发出，`Content-Type` 为文件类型，带 CSRF 头。本地预览用 `FileReader.readAsDataURL`，CSP 不允许 `blob:`。
- **取图**：`GET /panel/api/persona-images/{id}?size=reply|original`，按存储的类型返回。`<img>` 用相对地址，带 `?v=<id>`。
- 约束照旧：不用 `innerHTML`，不用内联 `style`，不引用外部链接，文字一律用 `textContent`。

### 4.7 接口

面板接口（要求会话登录、同源和 CSRF）：

- `GET /panel/api/persona-templates`：模板列表。
- `GET /panel/api/persona-templates/{name}`：单个模板，带来源和配置原版（如果有）。
- `PUT /panel/api/persona-templates/{name}`：请求体 `{persona, reason}`。名字不存在就新建为面板模板。
- `POST /panel/api/persona-templates/{name}/reset`
- `DELETE /panel/api/persona-templates/{name}`
- `POST /panel/api/persona-images`：原始图片作为请求体，返回 `{id, width, height, reply_tokens}`。
- `GET /panel/api/persona-images/{id}`
- `POST /panel/api/persona-images/{id}/describe`：返回 `{text}`。

现有接口：

- 会话人设的 `PUT` 和网关的人设接口会收到新字段，并按 4.1 校验图片 ID。
- 网关不提供图片上传。

### 4.8 文档和配置

- `docs/configuration.md`：人设新增字段、`model_profiles.vision`、模板在线覆盖规则。
- `docs/panel.md`：人设模板页、接口、审计种类。
- `docs/architecture.md`：模板目录、图片表、回复请求里图片的位置和 token 计算、识图流程。
- `docs/deployment.md`：数据库会因为图片变大；推荐的 `vision` 配置。
- `conloom.example.yaml`：在注释里给出 `vision` 示例（模型名用占位符，不写具体厂商的模型名）、`appearance` 示例（用通用描述）、`short_context` 的 `modalities` 说明。

## 5. 留给实施者

- 缩小算法的具体写法（区域平均即可）。
- 共用人设表单组件怎么拆。
- 识图提示词的具体措辞，前提是满足 4.4 的要求。
