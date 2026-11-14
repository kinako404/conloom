-- The channel a message was written in and an action is delivered through:
-- '' for the platform's adapter, 'panel' for the panel.
ALTER TABLE messages ADD COLUMN channel TEXT NOT NULL DEFAULT '';
ALTER TABLE outbox_actions ADD COLUMN channel TEXT NOT NULL DEFAULT '';
CREATE INDEX outbox_actions_channel ON outbox_actions (channel, status, conversation_key, seq);
