# Reply first, update state afterwards

## Intent

Replies of bots with skills are slow on the self-hosted thinking model: the
first request of every such reply offers tools and thinks (16–58 s observed,
600–1800 reasoning tokens), and when the model calls no tool that thinking
request *is* the reply. The user wants replies to come quickly while tool use
(especially state changes: bars, status, scene, memory) stays as careful as it
is with thinking on. So split each turn in two:

1. **Reply step** — fast: thinking off, only the tools whose results the reply
   needs (lookups).
2. **State step** — after the reply has been written, in the background, with
   the profile's default (thinking on): only the tools that change state, for
   the turn that just happened.

## Goals

- Every tool is either a **lookup** (result needed to write the reply) or a
  **state** tool (changes stored state; the reply does not need its result).
  Built-ins: lookups `recall`, `get_affinity`, `now`; state `remember`,
  `set_status`, `set_scene`, `adjust_buff`, `add_buff`, `remove_buff`. A tool
  declares it through an optional interface (e.g. `skills.StateChanger` with
  `ChangesState() bool`); tools that don't declare it (external tools from
  `skills.dir`) are lookups, i.e. keep today's behaviour.
- **Reply step**: the reply request offers only lookup tools and the skill
  section explains only what applies (state tools are not offered and the
  model is told the state is taken care of afterwards — wording left to the
  builder, must not make the model mention it). Thinking is turned off for all
  requests of the reply step via the existing `thinkingOff(extra_body)` (only
  where the profile has those keys). A reply with no lookup tools needed is a
  single fast request.
- **State step**: runs after the reply task has completed successfully and its
  result (actions) is written, when the task was a direct reply that offered
  state tools (same eligibility as today's skill run: bot has the skill,
  conversation type in the skill's `conversations`, private or addressed group
  reply; wake turns: see below). It is a separate model call with:
  - the same persona system prompt, a skill section covering only the state
    tools, and an instruction: judge from the latest turn (the trigger messages
    and the reply just written) whether any state should change; call the
    tools if so; write no reply text — any text is discarded;
  - the conversation context like a reply (history including the bot's new
    reply, the hidden state block, memory block), within the profile's budget;
  - the profile's default extra_body (thinking as configured);
  - the same `skills.Invocation` identity as the reply task (task id, speaker,
    conversation, trigger message ids), so `remember`'s "the person asked"
    check and `set_scene`'s same-task errand rule keep working;
  - the existing tool-loop machinery (rounds, per-call timeout, call limits),
    with its own time budget (`statePassTimeout`, e.g. 90 s) and the existing
    re-route on model failure; nothing is ever sent to the chat.
- **Ordering and concurrency**: at most one state step per conversation at a
  time. If another reply of the same conversation finishes while one runs, one
  more state step runs afterwards covering the newest turn (coalesce; never a
  queue of many). The next reply never waits for a state step; it reads
  whatever state is stored (a state change from the previous turn may land a
  few seconds later — accepted).
- **Cancellation**: no state step for replies that were cancelled, superseded,
  skipped (`[[SKIP]]`), blocked by review, or failed. Shutdown cancels running
  state steps. `/stop`, `/pause`, forgetting the person, deleting the chat:
  cancel the conversation's pending/running state step.
- **Wake turns** (status return, initiate, follow-up): today they offer only
  the buff tools. In the new mode the wake reply offers no tools (thinking
  off), and a state step with only the buff tools (`wakeTools`) runs after it,
  so statuses still cannot chain.
- **Switch**: config `skills.state_step: after_reply` (default) or `inline`
  (today's behaviour: one tool loop with all tools and the profile's thinking).
  Documented in configuration.md and conloom.example.yaml.
- **Observability**: state-step model calls are recorded like reply calls with
  purpose `state` (usage page shows them); log lines `state step` with task id,
  rounds, calls, duration, outcome; audit nothing new beyond what tools already
  write.

## Non-goals

- Changing which tools exist, their limits, or their texts beyond the skill
  section split.
- Making the reply wait for or reflect same-turn state changes.
- A panel UI for the switch (config only for now).

## Anti-goals

- Replies getting slower or the bot mentioning "updating state", tools or
  system.
- State changes applied twice (reply step and state step both calling them).
- A state step sending any message, or holding up the next reply.

## Approaches

- **A. Reply first, state step afterwards (chosen by the user).** Fast replies,
  careful state tools; cost: one extra model call per turn (self-hosted) and
  state changes visible from the next turn.
- B. Thinking off everywhere. Fastest, but tool use gets less reliable — the
  opposite of what the user wants.
- C. Keep today's single thinking tool loop. Reliable tools, slow replies.

## Constraints

- Public repo: fake data only; English conventional commits.
- Keep `inline` mode byte-identical to today (prompts, requests, behaviour).
- Respect existing limits/caps (admission, per-model concurrency, circuit
  breaker): state steps use background admission priority so they don't starve
  replies.
- Tests: tool classification; reply step offers only lookups and turns thinking
  off; state step runs after a successful reply with state tools only and same
  invocation, discards text, sends nothing; no state step for skip / cancel /
  supersede / failure; coalescing per conversation; wake turns; `inline` mode
  unchanged; shutdown/cancel paths.

## Left to the builder

Exact Go names and placement (a new `internal/core/statestep.go` is expected),
the Chinese wording of the reply-step note and the state-step instruction, how
the state step builds its context (reuse the reply's context builder), the
time budget value, and how coalescing is tracked.
