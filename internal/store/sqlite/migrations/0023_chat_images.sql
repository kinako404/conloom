-- Images sent in panel chats: a view copy (long edge <= 1536) and a reply copy (<= 768), both JPEG; no original.
-- message_id is '' until the message carrying the image is stored, which claims it in the same transaction.
CREATE TABLE chat_images (
    id               TEXT PRIMARY KEY,
    conversation_key TEXT NOT NULL,
    message_id       TEXT NOT NULL DEFAULT '',
    view_jpeg        BLOB NOT NULL,
    view_width       INTEGER NOT NULL,
    view_height      INTEGER NOT NULL,
    reply_jpeg       BLOB NOT NULL,
    reply_width      INTEGER NOT NULL,
    reply_height     INTEGER NOT NULL,
    reply_tokens     INTEGER NOT NULL,
    description      TEXT NOT NULL DEFAULT '',   -- cached vision description, '' when none
    created_by       TEXT NOT NULL,
    created_at       TEXT NOT NULL
);
CREATE INDEX chat_images_conversation_message ON chat_images (conversation_key, message_id);
CREATE INDEX chat_images_unsent ON chat_images (created_at) WHERE message_id = '';
