-- The settings of the bots defined in the config file, one row per bot,
-- changed in the panel. The config file keeps only their identity
-- (id, platform, platform_self_id); a config bot without a row runs with the
-- defaults. Lists are JSON arrays of strings.
CREATE TABLE bot_settings (
    bot_instance_id   TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    admins            TEXT NOT NULL,   -- platform user IDs
    private_chat      TEXT NOT NULL,   -- off | admins | allowlist | all
    private_allowlist TEXT NOT NULL,   -- platform user IDs
    enabled_groups    TEXT NOT NULL,   -- platform group IDs
    private_template  TEXT NOT NULL,   -- persona template of new private chats
    group_template    TEXT NOT NULL,   -- persona template of new groups
    skills            TEXT NOT NULL,   -- skill names
    updated_by        TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
