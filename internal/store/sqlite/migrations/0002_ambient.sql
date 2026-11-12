CREATE TABLE conversation_members (
    conversation_key  TEXT NOT NULL,
    platform_user_id  TEXT NOT NULL,
    last_engaged_at   TEXT NOT NULL,
    proactive_level   TEXT NOT NULL,
    level_set_by_user INTEGER NOT NULL,
    interests         TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    PRIMARY KEY (conversation_key, platform_user_id)
);

CREATE TABLE proactive_log (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_key TEXT NOT NULL,
    platform_user_id TEXT NOT NULL,
    kind             TEXT NOT NULL,
    task_id          TEXT NOT NULL,
    created_at       TEXT NOT NULL
);
CREATE INDEX proactive_log_conversation_created ON proactive_log (conversation_key, created_at);
