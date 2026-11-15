# 人设模板改为内置存储，支持 JSON 导出导入

日期：2026-11-15
状态：已定稿，待实施。部署者已确认：不再兼容配置文件里的人设模板，模板全部存在数据库里；导出的 JSON 带外观图，用 base64 存。标"实施者决定"的条目是我定的，部署者可以事后改。

## 1. 为什么做

模板现在有两层来源：配置文件和数据库里的在线版本。这让面板上多出"恢复为配置文件版本"、来源标注这些概念，模板也没法在部署之间搬来搬去。部署者希望：

- 模板只存在一个地方，就是数据库，只在面板里管理；
- 能把模板导出成 JSON 文件备份或分享，也能导回来。

## 2. 目标、非目标、反目标

**目标**

1. `conloom.yaml` 不再支持 `persona_templates`。
2. 升级后第一次启动，把配置文件里的模板自动导入数据库。之后配置里如果还写着 `persona_templates`，启动时报错，提示删掉这一段。
3. 模板目录只来自数据库。数据库里没有 `default` 时，用程序内置的默认人设。
4. 每个模板可以导出成一个 JSON 文件，外观图以 base64 嵌在里面；也可以一次导出全部模板。
5. 导入 JSON：单个模板文件和全部导出的文件都能导入。遇到同名模板时由部署者选择：覆盖，或者自动改名导入。
6. 审计：`persona.template.import`、`persona.template.export`、`persona.template.migrate`。

**非目标**

- 命令行导入导出。
- 导出会话自己的人设版本。会话人设仍然在会话页编辑；要把它变成模板，可以在会话页套用模板，或在模板页复制。
- 改变"会话第一次回复时复制模板，之后各自独立"的规则。

**反目标**

- 升级后模板丢失，或者在线改过的版本被配置文件里的旧版本覆盖。
- 导入时绕过校验：内容规则（`minor` 和 `adult` 不能同时为真）、字数上限、图片格式和尺寸上限都照常检查。
- 导入的图片不经处理直接入库。
- 导出文件里出现图片 ID 以外的内部信息，比如操作者、数据库时间以外的服务器信息、密钥。

## 3. 做法

只有一种合理做法：模板目录去掉配置层，迁移、导出、导入都基于现有的数据库表 `persona_templates` 和 `persona_images`。不新建模板表。

### 3.1 配置

- `Config.PersonaTemplates` 改名为只用于迁移的字段（比如 `LegacyPersonaTemplates`），配置加载时照常解析、照常校验内容（`Validate` + `ValidateContent`），但只交给启动迁移使用，运行时不再读它。
- 配置 bot 的 `persona_template` 不再在加载配置时检查是否存在，因为模板在数据库里。启动后由 bot 目录检查：模板不存在就记警告、运行时改用 `default`，面板上标"模板已不存在"。面板 bot 原本就是这个规则。
- `conloom.example.yaml` 删掉 `persona_templates` 整段，`default` 示例挪进文档。
- `docs/configuration.md` 写明：模板只在面板里管理；旧配置里的 `persona_templates` 在升级后第一次启动时自动导入，之后必须删除。

### 3.2 启动迁移

- 迁移 `0015_app_meta.sql` 新增键值表：

  ```sql
  CREATE TABLE app_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL);
  ```

- 启动时，在加载模板目录之前：
  - 配置里有 `persona_templates`，且 `app_meta` 里没有 `config_templates_migrated`：
    - 逐个写入 `persona_templates`；同名模板在数据库里已存在的跳过，以数据库为准。
    - 写入 `config_templates_migrated`，值为迁移时间。
    - 整个过程放在一个事务里。
    - 每个导入或跳过的模板记一条日志；写一条审计 `persona.template.migrate`，记录导入了哪些、跳过了哪些。
    - 结尾记一条警告，提示删除配置里的 `persona_templates`。
  - 配置里有 `persona_templates`，且已经迁移过：启动失败，退出码 78，报错信息为中文，说明模板已经迁到数据库，请删除配置里的 `persona_templates`。
  - 配置里没有这一段：什么都不做。
- 配置模板如果引用了图片 ID，原样写入。图片不存在时，回复照现有规则不带图并记警告。

### 3.3 模板目录

- 来源只有两种：数据库模板，以及内置 `default`（数据库里没有 `default` 时才出现，来源标为 `builtin`）。
- 操作：
  - 保存：写数据库。
  - 删除：`default` 不能删；被配置 bot 或面板 bot 当作默认模板的，返回 409 并列出这些 bot。
  - "恢复"只保留给 `default`：删掉数据库里的 `default`，回到内置默认人设。
- 面板 bot 和配置 bot 共用同一个"被谁使用"的检查。
- `ReferencedImages()` 不再包含配置模板的图片，因为配置层已经不存在。

### 3.4 导出格式

```json
{
  "format": "conloom.persona-template",
  "version": 1,
  "exported_at": "2026-11-15T12:00:00Z",
  "templates": [
    {
      "name": "example",
      "persona": { "...": "PersonaDoc，appearance_image 保留原 ID" },
      "appearance_image": { "mime": "image/png", "data": "<原图 base64>" }
    }
  ]
}
```

- 单个模板导出也用这个格式，`templates` 里只有一项，方便导入代码只处理一种格式。
- 没有外观图时，省略 `appearance_image`。
- 只导出原图；缩小图在导入时重新生成。
- 下载文件名：
  - 单个模板：`persona-<模板名>.json`；
  - 全部模板：`persona-templates-<日期>.json`。
- 内置 `default`（数据库里没有时）也可以导出，导出的就是内置默认人设。

### 3.5 导入

- 请求体是导出的 JSON，原样上传，最大 64 MiB。（实施者决定）
- 先整体校验，任何一项不合法就整个拒绝，返回 400，一项都不写：
  - `format` 和 `version` 必须正确，版本只认 1；
  - 模板名符合现有规则；
  - 人设通过 `Validate` 和 `ValidateContent`；
  - 图片 base64 能解码，且通过 `imaging.Process` 的格式和尺寸检查；
  - 文件里同一个名字只能出现一次。
- 图片处理后的 ID 就是原图的 sha256。人设里的 `appearance_image` 改写为这个 ID；文件里写的 ID 和算出来的不一致时，以算出来的为准。
- 同名冲突，由参数 `on_conflict` 决定：
  - `fail`（默认）：有任何同名模板就返回 409，列出冲突的名字，什么都不写。
  - `overwrite`：覆盖同名模板。
  - `rename`：改名导入，在原名后加 `-2`、`-3`，取第一个没被占用的名字；名字超过 32 个字符时先截短再加后缀。
- 图片和模板在同一个事务里写入。导入后刷新模板目录和面板 bot 的问题标注。
- 返回每个模板最后用的名字，以及是新建还是覆盖。
- 上传和图片处理沿用现有的"一次只处理一张图"限制。正在处理时返回 409。

### 3.6 面板

模板页：

- 每个模板有"导出"按钮。下载用同源链接加 `download` 属性，CSP 不需要改。
- 页头有"导出全部"和"导入"。导入流程：
  1. 选文件后，先按 `fail` 提交。
  2. 返回 409 时，弹窗列出冲突的名字，三个选项：覆盖、改名导入、取消。
- 去掉"恢复为配置文件版本"，`default` 改为"恢复为内置默认"。
- 来源标注只剩"内置"（未保存过的 `default`）；其余模板不再显示来源。
- 继续遵守现有的面板约束：不用 `innerHTML`，不用内联样式，不引用外部 URL，文字一律用 `textContent`。

### 3.7 接口

- `GET /panel/api/persona-templates/{name}/export`
- `GET /panel/api/persona-templates/export`：导出全部模板。
- `POST /panel/api/persona-templates/import?on_conflict=fail|overwrite|rename`
- `POST /panel/api/persona-templates/{name}/reset`：只对 `default` 有效，其他模板返回 400。

### 3.8 文档

更新这些文档：

- `docs/configuration.md`
- `docs/panel.md`
- `docs/architecture.md`
- `docs/deployment.md`：升级说明，含迁移规则和必须删掉配置段。
- `docs/api.md`：错误码。
- `conloom.example.yaml`

## 4. 留给实施者

- 迁移字段的具体命名。
- 导入、导出代码放在哪个包。
- 面板弹窗的样式。
