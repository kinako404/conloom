-- Bars and effects a bot adds itself, kept like the other bars by memory
-- scope. A bot-created bar's value and changes live in conversation_buffs
-- and conversation_buff_events with the persona's bars; an effect has no
-- value and lasts until until_at.
CREATE TABLE conversation_custom_bars (
    conversation_key  TEXT NOT NULL,
    name              TEXT NOT NULL,
    baseline          INTEGER NOT NULL,
    half_life_seconds INTEGER NOT NULL,
    description       TEXT NOT NULL,
    task_id           TEXT NOT NULL,   -- the task that added it
    created_at        TEXT NOT NULL,
    PRIMARY KEY (conversation_key, name)
);

CREATE TABLE conversation_effects (
    conversation_key TEXT NOT NULL,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL,
    started_at       TEXT NOT NULL,
    until_at         TEXT NOT NULL,
    task_id          TEXT NOT NULL,    -- the task that added it
    PRIMARY KEY (conversation_key, name)
);
CREATE INDEX conversation_effects_until ON conversation_effects (until_at);

-- What a bar change was: adjust (of a value) | bar_add | bar_remove |
-- effect_add | effect_remove.
ALTER TABLE conversation_buff_events ADD COLUMN kind TEXT NOT NULL DEFAULT 'adjust';

-- A task makes each kind of change of a name at most once, so a re-run task
-- changes nothing twice; it may add a bar and then adjust or remove it.
-- Existing rows are all adjustments, unique by (task_id, name) already.
DROP INDEX conversation_buff_events_task;
CREATE UNIQUE INDEX conversation_buff_events_task ON conversation_buff_events (task_id, name, kind) WHERE task_id <> '';
