CREATE TABLE panel_sessions (
    token_hash       TEXT PRIMARY KEY,
    user             TEXT NOT NULL,
    csrf_token       TEXT NOT NULL,
    cred_fingerprint TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    last_seen_at     TEXT NOT NULL,
    expires_at       TEXT NOT NULL
);
CREATE INDEX panel_sessions_expires ON panel_sessions (expires_at);
