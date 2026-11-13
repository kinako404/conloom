CREATE TABLE model_calls (
    id                INTEGER PRIMARY KEY,
    at                TEXT NOT NULL,
    bot_instance_id   TEXT NOT NULL,
    conversation_key  TEXT NOT NULL,
    task_id           TEXT NOT NULL,
    purpose           TEXT NOT NULL,
    role              TEXT NOT NULL,
    model             TEXT NOT NULL,
    prompt_tokens     INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    reasoning_tokens  INTEGER NOT NULL,
    estimated_tokens  INTEGER NOT NULL,
    latency_ms        INTEGER NOT NULL,
    outcome           TEXT NOT NULL,
    finish_reason     TEXT NOT NULL
);
CREATE INDEX model_calls_at ON model_calls (at);
CREATE INDEX model_calls_task ON model_calls (task_id) WHERE task_id <> '';
-- Covers the usage statistics: rows come out grouped by 15-minute slot and
-- dimensions, so aggregating them needs neither a sort nor table lookups.
CREATE INDEX model_calls_slot ON model_calls (
    unixepoch(at) / 900, bot_instance_id, purpose, role, model,
    at, outcome, prompt_tokens, completion_tokens, reasoning_tokens, latency_ms
);
