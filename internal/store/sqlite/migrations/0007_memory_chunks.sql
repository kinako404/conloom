CREATE TABLE memory_chunks (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_key TEXT NOT NULL,
    start_seq        INTEGER NOT NULL,
    end_seq          INTEGER NOT NULL,
    start_at         TEXT NOT NULL,
    end_at           TEXT NOT NULL,
    text_hash        TEXT NOT NULL,
    model            TEXT NOT NULL,
    dims             INTEGER NOT NULL,
    vector           BLOB NOT NULL,
    created_at       TEXT NOT NULL
);
CREATE INDEX memory_chunks_conversation ON memory_chunks (conversation_key, end_seq);
CREATE INDEX memory_chunks_model ON memory_chunks (model);

CREATE TABLE memory_chunk_state (
    conversation_key TEXT PRIMARY KEY,
    cursor_seq       INTEGER NOT NULL,
    model            TEXT NOT NULL
);

CREATE TABLE memory_chunk_rebuild (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_key TEXT NOT NULL,
    start_seq        INTEGER NOT NULL,
    end_seq          INTEGER NOT NULL,
    UNIQUE (conversation_key, start_seq, end_seq)
);

CREATE TABLE memory_chunk_forgets (
    conversation_key TEXT NOT NULL,
    platform_user_id TEXT NOT NULL,
    forgotten_at     TEXT NOT NULL,
    PRIMARY KEY (conversation_key, platform_user_id)
);
