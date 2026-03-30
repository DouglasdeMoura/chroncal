-- +goose Up
-- Alarm state tracking for todo alarms (mirrors alarm_state for events)
CREATE TABLE todo_alarm_state (
    id         INTEGER PRIMARY KEY,
    alarm_id   INTEGER NOT NULL REFERENCES todo_alarms(id) ON DELETE CASCADE,
    todo_id    INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    trigger_at TEXT NOT NULL,
    fired_at   TEXT,
    acked_at   TEXT,
    snoozed_to TEXT
);

CREATE UNIQUE INDEX idx_todo_alarm_state_unique ON todo_alarm_state(alarm_id, trigger_at);
CREATE INDEX idx_todo_alarm_state_todo_id ON todo_alarm_state(todo_id);

-- View to union both event and todo alarm states for unified queries
CREATE VIEW all_alarm_states AS
SELECT 
    'event' as type,
    id,
    alarm_id,
    event_id as item_id,
    trigger_at,
    fired_at,
    acked_at,
    snoozed_to
FROM alarm_state
UNION ALL
SELECT 
    'todo' as type,
    id,
    alarm_id,
    todo_id as item_id,
    trigger_at,
    fired_at,
    acked_at,
    snoozed_to
FROM todo_alarm_state;

-- +goose Down
DROP VIEW IF EXISTS all_alarm_states;
DROP INDEX IF EXISTS idx_todo_alarm_state_todo_id;
DROP INDEX IF EXISTS idx_todo_alarm_state_unique;
DROP TABLE IF EXISTS todo_alarm_state;
