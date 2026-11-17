-- The default policies of conversations without a policy of their own, one
-- series of versions per kind of conversation (group | private), changed in
-- the panel; every change appends a version as group_policies does. policy is
-- the JSON of a GroupPolicy.
CREATE TABLE policy_defaults (
    kind       TEXT NOT NULL,
    version    INTEGER NOT NULL,
    policy     TEXT NOT NULL,
    reason     TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (kind, version)
);

-- follows_defaults marks a policy version that returns the conversation to
-- the default policy of its kind: while its latest version has it, the
-- conversation is resolved as if it had no policy of its own. Older versions
-- are kept and the version numbers keep counting.
ALTER TABLE group_policies ADD COLUMN follows_defaults INTEGER NOT NULL DEFAULT 0;
