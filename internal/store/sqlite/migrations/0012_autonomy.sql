-- Private-chat autonomy: one row per conversation and timer kind, the
-- bot's current status, its hidden bars and their changes.
CREATE TABLE conversation_timers (
    conversation_key TEXT NOT NULL,
    kind             TEXT NOT NULL,    -- status_return | initiate | follow_up
    bot_instance_id  TEXT NOT NULL,
    platform_user_id TEXT NOT NULL,
    due_at           TEXT NOT NULL,
    anchor_seq       INTEGER NOT NULL, -- newest messages.seq of the conversation when armed
    lease_until      TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    PRIMARY KEY (conversation_key, kind)
);
CREATE INDEX conversation_timers_due ON conversation_timers (due_at);

CREATE TABLE conversation_status (
    conversation_key TEXT PRIMARY KEY,
    activity         TEXT NOT NULL,
    started_at       TEXT NOT NULL,
    until_at         TEXT NOT NULL,
    task_id          TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE conversation_buffs (
    conversation_key TEXT NOT NULL,
    name             TEXT NOT NULL,
    value            REAL NOT NULL,
    day              TEXT NOT NULL,
    day_delta        INTEGER NOT NULL,
    updated_at       TEXT NOT NULL,
    PRIMARY KEY (conversation_key, name)
);

CREATE TABLE conversation_buff_events (
    id               INTEGER PRIMARY KEY,
    conversation_key TEXT NOT NULL,
    name             TEXT NOT NULL,
    task_id          TEXT NOT NULL,
    source           TEXT NOT NULL,    -- model | panel | api
    before           REAL NOT NULL,
    after            REAL NOT NULL,
    reason           TEXT NOT NULL,
    created_at       TEXT NOT NULL
);
CREATE UNIQUE INDEX conversation_buff_events_task ON conversation_buff_events (task_id, name) WHERE task_id <> '';
CREATE INDEX conversation_buff_events_conv ON conversation_buff_events (conversation_key, created_at);
