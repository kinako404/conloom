# Chat scene: online / in person

## Intent

In a private chat the bot and the person are either talking through a chat app
("online") or are physically together ("in person"). Today the bot only knows
the first: the core prompt says it chats on a chat platform, and lines like
"（hands the bot some money）" are just text. Role-play in private chats
already drifts into in-person scenes (walking to the canteen, handing over
snacks) and back out (the bot leaves to fetch food), and the bot handles it
inconsistently.

The scene makes that explicit: a small, persistent piece of state the bot
switches itself as the conversation moves, which shapes how it talks. It is
built as a skill, the first step towards a later "virtual world" (places,
world settings); the stored shape must leave room for that without building it
now.

## Goals

- A built-in skill `scene` (opt-in per bot like `status`) with one tool,
  `set_scene(mode, place)`. `mode` is `online` or `in_person`; `place` is free
  text, optional, at most 16 runes, only meaningful in person.
- The bot switches the scene itself, from the conversation: meeting up →
  in person; leaving to do something → online; coming back → in person.
- Every private reply of a bot with the skill carries the current scene in the
  hidden state block, so the bot stays consistent.
- In person, the bot may add short actions or expressions in full-width
  brackets; online it does not.
- Proactive and wake messages follow the scene: online it messages the person;
  in person it speaks up beside them.
- The panel shows the scene and lets the admin change it.

## Non-goals

- Group chats. Groups are always online; the skill is private-only.
- A world model: place lists, maps, travel times, other characters' locations.
  The place is free text.
- Changing how Telegram/QQ deliver text: messages stay plain text there.

## Anti-goals

- The bot announcing or explaining the scene ("we are now in person"), or
  mentioning tools/system. It just talks accordingly.
- Flip-flopping: switching scene on every turn or on ambiguous hints. Switch
  only when the conversation clearly says the two met or parted.
- Being stuck "in person" for days after the person silently left.

## Approaches considered

- **A. Separate `scene` skill with its own stored state (chosen by the user).**
  Long-lived state, visible and editable in the panel, extendable into a
  world later.
- B. Fold into `status` (activity "beside you"). Statuses expire and mean
  "busy doing something"; a scene is long-lived. Mixing them blocks the world
  extension.
- C. No state, only a prompt rule letting the model infer the scene from
  history each turn. Free, but inconsistent and invisible.

## Design

### Storage

Migration `0022_scene.sql`: table `conversation_scene`, keyed like
`conversation_status` by the memory scope (`memory.ScopeKeyOf`, shared by a
bot's panel chats):

- `conversation_key` TEXT PRIMARY KEY (the scope)
- `mode` TEXT NOT NULL (`online` | `in_person`)
- `place` TEXT NOT NULL DEFAULT ''
- `since_at` (when this mode/place started)
- `touched_at` (last time the person spoke while in person; see below)
- `prev_mode`, `prev_place` (the scene before the latest switch, used to
  restore in person after an errand; see "Coming back")
- `updated_at`
- `origin_conversation_key` (the chat that set it, like status 0016)

Store methods `GetScene`, `SetScene`, `ClearScene`; the table joins the same
cleanup/migration lists as `conversation_status` (`autonomyTables`,
`delete.go`, `scope_migrate.go`). No row means online.

### Effective scene and the silence fallback

Only the person's messages keep an in-person scene alive; the bot's own
replies, proactive and wake messages do not (otherwise an in-person bot could
keep itself in person forever). `touched_at` is set by `set_scene` and
refreshed whenever a private task answering a person's message (any chat in
the scope) reads the scene while it is in person.

When a read finds `in_person` with `now - touched_at >= 3h` (constant
`sceneIdleTimeout`), the fallback is written back: the scene becomes online
(prev = the in-person scene), before the touch for the current message, so a
"good morning" after a day apart starts online. The read that writes it back is
the reply path (`stateBlock`); the panel and API reads compute the same
effective scene without writing and show it as online with a note ("超过 3
小时没说话，已回到线上").

### Tool and skill text

`internal/skills/tools/scene.go`, modelled on `status.go`:

- private chats only (same check as `set_status`);
- `mode` must be one of the two values; `place` is cleaned with the same
  helper as status activity (`CleanActivity` or a shared equivalent), passed
  through the output reviewer, and dropped when mode is `online`;
- setting the same mode and place as now is a no-op success (keeps
  `since_at`);
- the tool result is a short confirmation the model must not quote.

`internal/skills/builtin/scene/SKILL.md` (Chinese, in the voice of the other
built-ins), rules roughly:

- switch to in person only when the conversation clearly says you met or are
  together (arrived, came over, sitting next to each other); switch to online
  when one of you clearly leaves; do not switch on hints or hypotheticals;
- when you leave to do something that takes a while, call `set_scene online`
  together with `set_status`; when you come back to the person, call
  `set_scene in_person`;
- in person you may add a short action or expression in full-width brackets,
  at most one per message, not every message; online, no actions — only what
  you would type;
- never mention the scene, tools or system.

Every switch records the scene it replaced in `prev_mode`/`prev_place`.

### Coming back from an errand

Wake turns do not offer `set_scene` (they keep offering only the buff tools).
Instead, the return is deterministic: when a `status_return` wake fires and the
stored scene is online with `prev_mode = in_person`, and the switch to online
happened at or after the status started (allow 2 minutes of slack, since the
two tools are usually called in the same turn), the scene is restored to in
person at `prev_place` (`touched_at = now`) before the return message is
built. The status-return directive then uses its in-person variant ("you are
back beside them"). If the bot forgot to switch to online when leaving, the
scene simply stays in person.

### Prompt

`autonomy.RenderState` gets the scene as its first line when the bot enables
`scene` (always, including online, so the model knows the default):

- online: `场景：线上（你们隔着聊天软件发消息，不在一起）`
- in person: `场景：面对面，在<place>（你们在同一个地方；可以偶尔用括号写一个简短的动作或神态）`
  (without `，在<place>` when place is empty)

The scene line has priority under the 300-rune cap: it is never dropped
(the place is already ≤16 runes). The existing header/strip rules already cover
echoes of the block.

The proactive/initiate/follow-up/status-return directives in `copy.go` get an
in-person variant ("you are together with them; you speak up beside them")
used when the effective scene is in person.

### Panel

- Autonomy tab (`views/autonomy.js`): a "场景" section above status showing
  mode, place, since, and the fallback note; controls to switch to online or
  in person (with a place input). Writes go through a new
  `PUT /panel/api/conversations/{key}/autonomy/scene` with the usual CSRF and
  audit (`autonomy.scene.set`). Only shown when the bot enables `scene`;
  otherwise a hint that the skill is off.
- `#/im` chat header: under the name, when the effective scene is in person,
  show `面对面 · <place>` (or `面对面`); the typing indicator takes precedence
  while it shows. `getChat` JSON gains `scene`.
- `#/im` message bubbles: full-width bracket segments `（…）` in bot and user
  messages render in a muted colour (class `tg-action`), text otherwise
  unchanged and still escaped. This is the panel only. (My call; the user did
  not decide this.)

### API and docs

- `AutonomyService.State` and the gateway autonomy read include the effective
  scene. Like the other autonomy writes, there is both a panel route and an API
  route `PUT /api/v1/conversations/{id}/autonomy/scene` with a `pkg/client`
  method; core takes the panel-or-API source for audit.
- `docs/skills.md` (new skill section), `docs/architecture.md` (state block),
  `docs/panel.md`, `docs/api.md`.

## Constraints

- Public repo: no real IDs, names, hosts or character names in code, tests,
  docs or commit messages. Commit messages in English conventional style.
- Follow existing patterns (`status` tool, `0016` origin column, autonomy
  panel tab, web tests that check source strings).
- Tests for: tool validation and private-only check, store CRUD and cleanup,
  effective-scene fallback, RenderState scene line and cap priority, wake tool
  availability, directive variants, panel API + audit, web source tests.

## Left to the builder

Exact Go names, the precise Chinese wording of SKILL.md and directive variants,
how the latest message time is looked up for the fallback, and the panel
control layout.
