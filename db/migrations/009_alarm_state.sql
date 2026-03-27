-- +goose Up
CREATE TABLE alarm_state (
    id         INTEGER PRIMARY KEY,
    alarm_id   INTEGER NOT NULL REFERENCES event_alarms(id) ON DELETE CASCADE,
    event_id   INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    trigger_at TEXT NOT NULL,
    fired_at   TEXT,
    acked_at   TEXT,
    snoozed_to TEXT
);
CREATE UNIQUE INDEX idx_alarm_state_unique ON alarm_state(alarm_id, trigger_at);
CREATE INDEX idx_alarm_state_event_id ON alarm_state(event_id);

-- +goose Down
DROP INDEX IF EXISTS idx_alarm_state_event_id;
DROP INDEX IF EXISTS idx_alarm_state_unique;
DROP TABLE IF EXISTS alarm_state;
