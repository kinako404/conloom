-- The channel a message was written in and an action is delivered through:
-- '' for the platform's adapter, 'panel' for the panel. The adapters' lease
-- keeps to one bot's platform channel; the panel's spans every bot.
ALTER TABLE messages ADD COLUMN channel TEXT NOT NULL DEFAULT '';
ALTER TABLE outbox_actions ADD COLUMN channel TEXT NOT NULL DEFAULT '';
DROP INDEX outbox_actions_lease;
CREATE INDEX outbox_actions_lease ON outbox_actions (bot_instance_id, channel, status, conversation_key, seq);
CREATE INDEX outbox_actions_channel ON outbox_actions (channel, status, conversation_key, seq);
