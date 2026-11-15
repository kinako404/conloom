-- The chat a status was set in, whose status_return timer comes back to
-- talk: a status of a bot's shared panel memory scope is kept under the
-- scope's key. '' is the status's own conversation.
ALTER TABLE conversation_status ADD COLUMN origin_conversation_key TEXT NOT NULL DEFAULT '';
