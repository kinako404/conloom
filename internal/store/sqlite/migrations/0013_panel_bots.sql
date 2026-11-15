-- Bots created in the panel. Each also has a bot_instances row (platform
-- 'panel'), which is kept when the bot is deleted.
CREATE TABLE panel_bots (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    persona_template TEXT NOT NULL,
    skills           TEXT NOT NULL,    -- JSON array of skill names
    created_by       TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
