-- +goose Up

-- Operational state for event alarms and recurrence expansion.

-- Tracks alarm firing, acknowledgement, and snooze state.
CREATE TABLE alarm_state (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id   INTEGER NOT NULL REFERENCES event_alarms(id) ON DELETE CASCADE,
    event_id   INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    trigger_at TEXT    NOT NULL,
    fired_at   TEXT,
    acked_at   TEXT,
    snoozed_to TEXT
);

CREATE UNIQUE INDEX idx_alarm_state_unique     ON alarm_state(alarm_id, trigger_at);
CREATE INDEX        idx_alarm_state_event_id   ON alarm_state(event_id);
CREATE INDEX        idx_alarm_state_trigger_at ON alarm_state(trigger_at);
CREATE INDEX        idx_alarm_state_snoozed    ON alarm_state(snoozed_to) WHERE snoozed_to IS NOT NULL;

-- Materialized recurrence instances for query-time expansion.
CREATE TABLE recurrence_instances (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    original_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    instance_at TEXT    NOT NULL,
    is_override INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE UNIQUE INDEX idx_recurrence_unique     ON recurrence_instances(event_id, instance_at);
CREATE INDEX        idx_recurrence_event      ON recurrence_instances(event_id);
CREATE INDEX        idx_recurrence_instance_at ON recurrence_instances(instance_at);
CREATE INDEX        idx_recurrence_original   ON recurrence_instances(original_id);

-- +goose Down
DROP TABLE IF EXISTS recurrence_instances;
DROP TABLE IF EXISTS alarm_state;
