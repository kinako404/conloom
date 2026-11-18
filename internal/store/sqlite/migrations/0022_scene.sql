-- Where the bot and the person of a private chat are, kept like
-- conversation_status under the memory scope: no row means online.
-- touched_at is when the scene was set or the person last spoke in person;
-- prev_mode and prev_place are the scene the latest switch replaced.
-- origin_conversation_key is the chat it was set in, '' the scope's own.
CREATE TABLE conversation_scene (
    conversation_key        TEXT PRIMARY KEY,           -- memory scope
    mode                    TEXT NOT NULL,              -- online | in_person
    place                   TEXT NOT NULL DEFAULT '',
    since_at                TEXT NOT NULL,
    touched_at              TEXT NOT NULL,
    prev_mode               TEXT NOT NULL DEFAULT '',
    prev_place              TEXT NOT NULL DEFAULT '',
    updated_at              TEXT NOT NULL,
    origin_conversation_key TEXT NOT NULL DEFAULT ''
);
