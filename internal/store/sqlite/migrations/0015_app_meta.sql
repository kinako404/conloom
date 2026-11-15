-- Facts about this database as a whole, such as when the config file's
-- persona templates were migrated into it (config_templates_migrated).
CREATE TABLE app_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL);
