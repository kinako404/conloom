-- Keyword index of messages: rowid is messages.seq, conv the conversation's
-- mark (search.ConversationMark) and seg the tokens of the text and of the
-- sender's name (search.IndexText). Searches filter conv by column, so a mark
-- written in a message's text never matches it. Contentless, so the tokens
-- are not stored twice; contentless_delete allows rows to be replaced or
-- deleted.
CREATE VIRTUAL TABLE message_fts USING fts5(conv, seg, content='', contentless_delete=1, tokenize='unicode61');

-- Every message with seq <= backfilled_seq is in message_fts.
CREATE TABLE message_fts_progress (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    backfilled_seq INTEGER NOT NULL
);
INSERT INTO message_fts_progress (id, backfilled_seq) VALUES (1, 0);
