CREATE TABLE group_memory (
    conversation_key TEXT PRIMARY KEY,
    summary          TEXT NOT NULL,
    cursor_seq       INTEGER NOT NULL,
    last_run_at      TEXT NOT NULL,
    last_error       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE member_memory (
    conversation_key     TEXT NOT NULL,
    platform_user_id     TEXT NOT NULL,
    display_name         TEXT NOT NULL,
    note                 TEXT NOT NULL,
    affinity             INTEGER,
    last_interaction_day TEXT NOT NULL,
    model_delta_day      TEXT NOT NULL,
    model_delta_sum      INTEGER NOT NULL,
    decay_steps          INTEGER NOT NULL,
    forgotten_at         TEXT NOT NULL,
    version              INTEGER NOT NULL,
    updated_at           TEXT NOT NULL,
    PRIMARY KEY (conversation_key, platform_user_id)
);

CREATE TABLE memory_revisions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_key TEXT NOT NULL,
    platform_user_id TEXT NOT NULL,
    field            TEXT NOT NULL,
    old_value        TEXT NOT NULL,
    new_value        TEXT NOT NULL,
    source           TEXT NOT NULL,
    reason           TEXT NOT NULL,
    created_at       TEXT NOT NULL
);
CREATE INDEX memory_revisions_member ON memory_revisions (conversation_key, platform_user_id);
CREATE INDEX memory_revisions_created ON memory_revisions (created_at);
