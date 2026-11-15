-- Appearance reference images, never changed once stored, and persona
-- templates written online, which override or add to the configured ones.
CREATE TABLE persona_images (
    id           TEXT PRIMARY KEY, -- hex sha256 of the original bytes
    mime         TEXT NOT NULL,    -- image/png | image/jpeg
    width        INTEGER NOT NULL,
    height       INTEGER NOT NULL,
    original     BLOB NOT NULL,
    reply_jpeg   BLOB NOT NULL,    -- reply copy, long edge <= 768 px, JPEG quality 85
    reply_width  INTEGER NOT NULL,
    reply_height INTEGER NOT NULL,
    reply_tokens INTEGER NOT NULL, -- token estimate of the reply copy
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL
);

CREATE TABLE persona_templates (
    name       TEXT PRIMARY KEY,
    persona    TEXT NOT NULL,      -- PersonaDoc JSON
    updated_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
