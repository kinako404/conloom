-- A conversation's actions newest first, for the last reply of a user
-- (LastReplyTask), which runs for every private message.
CREATE INDEX outbox_actions_conversation ON outbox_actions (conversation_key, seq);

-- The actions not yet delivered by when they expire, for the expiry that
-- runs every 30 seconds. Only a query holding the condition word for word
-- (liveOutbox in outbox.go) uses it, so the lease queries keep to their
-- own indexes.
CREATE INDEX outbox_actions_live ON outbox_actions (expires_at) WHERE status IN ('pending', 'leased');
