# 机器人自己增减状态条和效果

日期：2026-11-16
状态：已定稿，部署者已确认"两种都要，开工"。标"实施者决定"的条目是我定的，部署者可以事后改。

## 1. 要做什么

现在状态条的种类写在人设里，最多 4 条，机器人只能调整这几条的数值。部署者希望机器人能自己增减种类，两种都要：

- **自建状态条**：和"心情"一样是 0–100 的数值，有平常水平，会慢慢回落到平常水平。比如"饥饿""困意"。机器人加上以后，可以用现有的 `adjust_buff` 调整数值，不需要时自己删掉。
- **效果**：只有名字、一句说明和持续时间，到时间自动消失。比如"着凉，2 小时""吃饱了，1 小时"。机器人也可以提前去掉。

人设里写好的状态条保持不变，机器人不能删。

## 2. 规则

**数量上限（实施者决定）**：同一个记忆范围里，自建状态条最多 6 条，同时生效的效果最多 6 个。记忆范围和其他状态一样：面板聊天按 bot 共用，平台对话按对话。

**名字**：

- 1–6 个字，规则和人设状态条相同：不能有空白、`【】`、冒号或不可见字符。
- 不能和人设里的状态条、已有的自建状态条或正在生效的效果重名。
- 名字和说明在保护模式下要过 `OutputProtected`，在开放模式下要过 `Output`，被拦下就不存。和 `set_status` 的处理一样。

**说明**：0–40 字，清理成一行。

**自建状态条的参数**：

- 平常水平：0–100。
- 回落速度：半衰期 30 分钟到 7 天，用小时传入，默认 6 小时。
- 初始值：0–100，默认等于平常水平。
- 数值的计算、显示、每日调整上限、面板修改，全部和人设状态条一样，复用现有的数值表和事件表。

**效果的时长**：5 分钟到 7 天，用分钟传入。到期以后不再显示，由每小时的维护任务删除。

**人设后来新增了同名的状态条**：人设优先，同名的自建状态条不再显示，也不能再调整。

**"忘记"和删除**：

- "忘记"会和清状态条一样，一起清掉自建状态条和效果。
- 删除对话、删除面板 bot、删除记忆范围时一并删除。

## 3. 工具

两个新工具，归入现有技能 `buff`，所以启用了 `buff` 的机器人自动就有：

**`add_buff(kind, name, description, ...)`**

- `kind=bar`：参数是 `baseline`、`half_life_hours`、`value`。
- `kind=effect`：参数是 `minutes`。
- 返回一句话，说明加了什么。返回里不出现数值，只用档位词和剩余时长。

**`remove_buff(name)`**

- 能删自建状态条和效果。
- 写的是人设状态条的名字时，返回"这一条是固定的，不能删。"

**`adjust_buff`**：现在也接受自建状态条的名字。

机器人主动发起的回合原本只给 `adjust_buff`，现在 `buff` 技能里的三个工具都给。`set_status` 仍然不给。

技能说明（`SKILL.md`）要写清楚：

- 什么时候加数值条：需要长期跟踪的东西，比如饥饿、困意。
- 什么时候加效果：一段时间内的临时状况，比如着凉、刚吃饱、头晕。
- 种类保持少量，不再相关就删掉。
- 不要向对方提起这些东西。

## 4. 存储

迁移 `0017_dynamic_buffs.sql`：

```sql
CREATE TABLE conversation_custom_bars (
  conversation_key TEXT NOT NULL, name TEXT NOT NULL,
  baseline INTEGER NOT NULL, half_life_seconds INTEGER NOT NULL, description TEXT NOT NULL,
  task_id TEXT NOT NULL, created_at TEXT NOT NULL,
  PRIMARY KEY (conversation_key, name));
CREATE TABLE conversation_effects (
  conversation_key TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL,
  started_at TEXT NOT NULL, until_at TEXT NOT NULL, task_id TEXT NOT NULL,
  PRIMARY KEY (conversation_key, name));
CREATE INDEX conversation_effects_until ON conversation_effects (until_at);
ALTER TABLE conversation_buff_events ADD COLUMN kind TEXT NOT NULL DEFAULT 'adjust';
```

- `kind` 的取值：`adjust`、`bar_add`、`bar_remove`、`effect_add`、`effect_remove`。
- 新增和删除也记成事件，面板的"最近变化"里能看到。
- 名字已经被占用时，同一个 `task_id` 重复执行不报错，重启后重跑任务也只生效一次。

新表都要加进：

- `conversationTables`（`delete_test` 的覆盖测试会检查）；
- `ForgetPrivate`、`ForgetPrivateScope`；
- `DeleteScope`；
- "迁移只新增表"的测试。

## 5. 提示和显示

**状态块**（`autonomy.RenderState`）：

- 状态条：先列人设的，再按创建顺序列自建的。格式不变，都是"名字：档位（说明）"。
- 效果单独一行：`效果：着凉（还剩约 1 小时 20 分；说明）、吃饱了（还剩约 30 分）`。
- 300 字上限照旧。超出时按这个顺序删减：先删说明，再删效果，再删排在最后的状态条，最后截短"正在"那一行。

**清理模型抄回来的状态块**：`StripTranscriptPrefix` 的状态块规则加一种行，即以"效果："开头的行。同时补测试，确认"效果：…"出现在正常回复正文里时不会被删。

**面板"自主"标签页**：

- 状态条区域列出人设的和自建的，自建的标"自建"，带"删除"按钮。
- 新增"效果"区域：列出名字、说明、剩余时间和"删除"按钮。
- 面板只能删除，不能新增：新增交给机器人。（实施者决定）
- 删除记审计：`autonomy.bar.remove`、`autonomy.effect.remove`。

**接口**：

- `DELETE /panel/api/conversations/{key}/autonomy/bars/{name}`：只能删自建状态条，删人设状态条返回 400。
- `DELETE /panel/api/conversations/{key}/autonomy/effects/{name}`
- 网关同样提供这两个接口，权限是 `memory:write`。
- `GET …/autonomy` 的返回加两项：`custom_bars` 是状态条上的标记，`effects` 是新的列表。

**文档**：更新 `docs/skills.md`、`docs/architecture.md`、`docs/panel.md`、`docs/api.md`、`docs/usage.md`。

## 6. 留给实施者

- 工具参数的具体 JSON 结构（满足第 3 节）。
- 剩余时长的显示措辞。
- 面板的布局。
