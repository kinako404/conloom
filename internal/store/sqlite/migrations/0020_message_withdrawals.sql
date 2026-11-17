-- withdrawals counts how often messages of the conversation were withdrawn
-- (DeleteMessagesFrom). It is part of the conversation's forget stamp, so
-- chunks the semantic search index cut before a withdrawal are never stored.
ALTER TABLE conversations ADD COLUMN withdrawals INTEGER NOT NULL DEFAULT 0;
