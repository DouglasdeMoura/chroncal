-- +goose Up

-- Operational state for todo alarms and recurrence expansion.

-- Tracks alarm firing, acknowledgement, and snooze state.
CREATE TABLE todo_alarm_state (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id   INTEGER NOT NULL REFERENCES todo_alarms(id) ON DELETE CASCADE,
    todo_id    INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    trigger_at TEXT    NOT NULL,
    fired_at   TEXT,
    acked_at   TEXT,
    snoozed_to TEXT
);

CREATE UNIQUE INDEX idx_todo_alarm_state_unique     ON todo_alarm_state(alarm_id, trigger_at);
CREATE INDEX        idx_todo_alarm_state_todo_id    ON todo_alarm_state(todo_id);
CREATE INDEX        idx_todo_alarm_state_trigger_at ON todo_alarm_state(trigger_at);
CREATE INDEX        idx_todo_alarm_state_snoozed    ON todo_alarm_state(snoozed_to) WHERE snoozed_to IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS todo_alarm_state;
