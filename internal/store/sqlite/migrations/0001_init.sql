CREATE TABLE bot_instances (
    id         TEXT PRIMARY KEY,
    platform   TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE conversations (
    bot_instance_id          TEXT NOT NULL,
    id                       TEXT NOT NULL,
    key                      TEXT NOT NULL UNIQUE,
    type                     TEXT NOT NULL,
    platform                 TEXT NOT NULL,
    platform_conversation_id TEXT NOT NULL,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    PRIMARY KEY (bot_instance_id, id)
);

CREATE TABLE users (
    bot_instance_id  TEXT NOT NULL,
    platform         TEXT NOT NULL,
    platform_user_id TEXT NOT NULL,
    display_name     TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    PRIMARY KEY (bot_instance_id, platform, platform_user_id)
);

CREATE TABLE events (
    bot_instance_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash    TEXT NOT NULL,
    event_id        TEXT NOT NULL,
    response        TEXT NOT NULL,
    raw_event       TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    PRIMARY KEY (bot_instance_id, idempotency_key)
);
CREATE INDEX events_created_at ON events (created_at);

CREATE TABLE messages (
    seq                 INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_instance_id     TEXT NOT NULL,
    id                  TEXT NOT NULL,
    conversation_key    TEXT NOT NULL,
    platform            TEXT NOT NULL,
    platform_message_id TEXT NOT NULL,
    actor_id            TEXT NOT NULL,
    platform_user_id    TEXT NOT NULL,
    display_name        TEXT NOT NULL,
    text                TEXT NOT NULL,
    reply_to            TEXT NOT NULL,
    mentions            TEXT NOT NULL,
    attachments         TEXT NOT NULL,
    from_bot            INTEGER NOT NULL,
    occurred_at         TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    UNIQUE (bot_instance_id, id)
);
CREATE UNIQUE INDEX messages_platform_message
    ON messages (bot_instance_id, platform, conversation_key, platform_message_id)
    WHERE platform_message_id <> '';
CREATE INDEX messages_conversation_seq ON messages (conversation_key, seq);

CREATE TABLE tasks (
    id                     TEXT PRIMARY KEY,
    bot_instance_id        TEXT NOT NULL,
    conversation_key       TEXT NOT NULL,
    status                 TEXT NOT NULL,
    actor_platform_user_id TEXT NOT NULL,
    command                TEXT NOT NULL,
    trigger_message_ids    TEXT NOT NULL,
    priority               INTEGER NOT NULL,
    meta                   TEXT NOT NULL,
    error_code             TEXT NOT NULL,
    event_occurred_at      TEXT NOT NULL,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);
CREATE INDEX tasks_status ON tasks (status);

CREATE TABLE outbox_actions (
    seq                 INTEGER PRIMARY KEY AUTOINCREMENT,
    action_id           TEXT NOT NULL UNIQUE,
    bot_instance_id     TEXT NOT NULL,
    conversation_key    TEXT NOT NULL,
    task_id             TEXT NOT NULL,
    platform            TEXT NOT NULL,
    status              TEXT NOT NULL,
    payload             TEXT NOT NULL,
    lease_until         TEXT NOT NULL,
    attempts            INTEGER NOT NULL,
    platform_message_id TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    expires_at          TEXT NOT NULL,
    acked_at            TEXT NOT NULL
);
CREATE INDEX outbox_actions_lease ON outbox_actions (bot_instance_id, status, conversation_key, seq);
CREATE INDEX outbox_actions_task ON outbox_actions (task_id);

CREATE TABLE persona_instances (
    conversation_key TEXT PRIMARY KEY,
    template_name    TEXT NOT NULL,
    created_at       TEXT NOT NULL
);

CREATE TABLE persona_versions (
    conversation_key TEXT NOT NULL,
    version          INTEGER NOT NULL,
    persona          TEXT NOT NULL,
    reason           TEXT NOT NULL,
    updated_by       TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    PRIMARY KEY (conversation_key, version)
);

CREATE TABLE group_policies (
    conversation_key TEXT NOT NULL,
    version          INTEGER NOT NULL,
    policy           TEXT NOT NULL,
    reason           TEXT NOT NULL,
    updated_by       TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    PRIMARY KEY (conversation_key, version)
);

CREATE TABLE audit_events (
    id              TEXT PRIMARY KEY,
    bot_instance_id TEXT NOT NULL,
    kind            TEXT NOT NULL,
    actor           TEXT NOT NULL,
    target          TEXT NOT NULL,
    detail          TEXT NOT NULL,
    created_at      TEXT NOT NULL
);
