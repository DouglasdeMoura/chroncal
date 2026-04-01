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

-- Materialized recurrence instances for query-time expansion.
CREATE TABLE todo_recurrence_instances (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id     INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    original_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    instance_at TEXT    NOT NULL,
    is_override INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE UNIQUE INDEX idx_todo_recurrence_unique     ON todo_recurrence_instances(todo_id, instance_at);
CREATE INDEX        idx_todo_recurrence_todo       ON todo_recurrence_instances(todo_id);
CREATE INDEX        idx_todo_recurrence_instance_at ON todo_recurrence_instances(instance_at);
CREATE INDEX        idx_todo_recurrence_original   ON todo_recurrence_instances(original_id);

-- +goose Down
DROP TABLE IF EXISTS todo_recurrence_instances;
DROP TABLE IF EXISTS todo_alarm_state;
