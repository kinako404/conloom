# Outer bars: bars the bot can show, with per-bar step limits

## Intent

Persona bars (persona doc `bars[]`, the `buff` skill) are hidden: the model
sees only a band word (很低…很高), is told they only affect its tone and must
never be mentioned, and can move a bar by at most ±10 per call and ±30 net per
day. That fits moods (心情, 好感) but not physical, visible states the user
models with bars, such as fullness (饥饿, 肚子) or blushing: the user wants a
large event to move such a bar a lot, and wants the bot's descriptions
(actions, appearance) to reflect its value. Today neither is possible by
design. This change lets a persona mark a bar as **outer** (visible) and set
its own step limits.

## Goals

- `PersonaBar` gains:
  - `kind`: `inner` (default; omitted or empty means inner — today's
    behaviour) or `outer`.
  - `max_step`: the most one `adjust_buff` call may move this bar, 1–100;
    0/omitted means the default 10.
  - `max_day`: the most this bar's net change may be per day, 1–100;
    0/omitted means the default 30. Must be ≥ the effective `max_step`.
- Inner bars render and behave exactly as today (byte-identical state block
  for personas without outer bars or custom limits).
- Outer bars, in the hidden state block of private replies:
  - are shown with their value and band, e.g. `肚子：80/100（很高）（说明）`;
  - sit under their own heading that says these are visible states which may
    be shown naturally in actions and appearance descriptions, without
    reporting numbers or mentioning bars/tools/system;
  - the block's existing header/footer wording ("只有你自己知道，不要直接说出来",
    "只影响你的语气") keeps applying to inner bars only, and must not
    contradict the outer section.
- `adjust_buff` enforces each persona bar's own `max_step`/`max_day`
  (defaults 10/30); the tool schema's delta range widens accordingly (±100),
  with the per-bar check in the tool and the limit stated in its rejection
  text. Custom bars the bot adds with `add_buff` stay inner with the default
  limits.
- The built-in `buff` SKILL.md explains outer bars (may be shown in
  descriptions, still no numbers/tools/system talk) and that step sizes follow
  each bar's limit (small things a little, big events up to the bar's limit).
- Panel:
  - persona form bar editor: a 类型 select (内在 / 外在, with a one-line hint
    each), 单次最多 and 每天最多 number inputs (blank = default 10 / 30);
  - profile card and the conversation autonomy tab mark outer bars (e.g. a
    small `外在` tag); the profile card shows numbers for all bars as today.
- Docs: configuration (persona fields), skills (buff), panel, architecture
  (state block), api (persona JSON) as applicable.

## Non-goals

- Thresholds that trigger scripted descriptions.
- Outer custom bars created by the model, or per-bar limits for custom bars.
- Changing effects, the status/scene lines, or panel/API `SetBar` (operators
  already set values freely).
- Any content-policy change: outer bars change only how bars are presented;
  persona content rules and minor protection still apply unchanged.

## Anti-goals

- Existing personas changing behaviour without opting in.
- The bot reciting numbers ("我的肚子现在是80") or mentioning bars/tools.
- Inner/outer instructions contradicting each other in the prompt.

## Approaches

- **A. Per-bar `kind` + per-bar limits on the persona bar (chosen with the
  user).** Small, explicit, opt-in per bar, no migration (optional JSON
  fields).
- B. A separate "body state" feature with its own tool. Duplicates bars,
  storage, decay and panel UI.
- C. Only rewording the global buff rules so all bars may be described.
  Breaks moods that should stay hidden and still can't move a bar far.

## Constraints

- Public repo: fake data only; English conventional commits.
- Persona validation in `pkg/conloomapi/persona.go` (`Validate`), exchange
  import/export and the persona form must carry the new fields.
- The state block's 300-rune cap: outer bars count like bars; the builder
  decides trim priority but outer bars' values must not be dropped before
  descriptions.
- Echo stripping (`contextbuilder/strip.go`) must cover any new heading line.
- Tests for: validation ranges and `max_day ≥ max_step`, state rendering
  (inner byte-identical; outer section), adjust_buff per-bar limits and
  rejection texts, daily cap per bar, form source tests, exchange round trip.

## Left to the builder

Exact Chinese wording of the outer heading and SKILL.md lines, Go names,
where the outer section sits inside the block, the form layout.

## Builder-check decisions (controller's calls)

1. **Over-limit deltas are clamped, not refused**, exactly like today's ±10
   clamp and partial daily-cap application (`buff.go` `dayCapped`) — only the
   numbers now come from the bar's `max_step`/`max_day` (defaults 10/30), so
   existing personas behave the same. The goal's "rejection text" means: the
   tool result says when a delta was cut down and to what limit; the existing
   "today's change is used up" rejection names the bar's daily limit.
2. **How the model learns limits:** the state block shows the step limit next
   to a bar only for outer bars and for bars with a non-default `max_step`
   (e.g. `肚子：80/100（很高，单次最多 ±40）（说明）`); inner bars with default
   limits render byte-identically. The shared `adjust_buff` description and
   SKILL.md are reworded generically (no "隐藏" absolutism; "幅度按状态条的单次
   上限，默认 ±10"; outer bars may be shown in descriptions) — a small prompt
   change for every buff bot, accepted. The limit text must survive the
   300-rune trimming as long as the bar line does.
3. **Panel chats sharing one memory scope:** values and the daily counter stay
   keyed by bar name in the scope (unchanged); `kind`, `max_step`, `max_day`
   come from the persona of the conversation being answered (as
   `scopePersonaBar` already does for baseline/half-life). `AutonomyBar` gains
   `kind`, `max_step`, `max_day` (omitempty) read from the viewed
   conversation's persona, for the autonomy tab and the profile card.
4. **Exchange format:** no version bump. The new fields are `omitempty`, so
   exports that don't use them still import on older builds; exports that do
   fail on older builds with the existing unknown-field error (documented in
   the persona exchange docs).
