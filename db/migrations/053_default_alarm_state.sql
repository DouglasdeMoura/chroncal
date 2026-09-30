-- +goose Up
-- Operational state for default (virtual) alarms, issue #815. A default
-- alarm has no event_alarms row: the alarm engine synthesizes it at check
-- time from the config defaults or the calendar override. alarm_state
-- cannot hold its firing state, because alarm_state.alarm_id references
-- event_alarms(id). This table mirrors alarm_state and keys the state by
-- the event, the configured trigger, and the absolute trigger time.
--
-- The (event_id, trigger_value, trigger_at) UNIQUE index gives
-- CreateDefaultAlarmState the same atomic-claim property that
-- idx_alarm_state_unique gives alarm_state: when two checkers overlap,
-- only the first INSERT wins.
CREATE TABLE default_alarm_state (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id      INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    action        TEXT    NOT NULL,
    trigger_value TEXT    NOT NULL,
    trigger_at    TEXT    NOT NULL,
    fired_at      TEXT,
    acked_at      TEXT,
    snoozed_to    TEXT
);

CREATE UNIQUE INDEX idx_default_alarm_state_unique     ON default_alarm_state(event_id, trigger_value, trigger_at);
CREATE INDEX        idx_default_alarm_state_event_id   ON default_alarm_state(event_id);
CREATE INDEX        idx_default_alarm_state_trigger_at ON default_alarm_state(trigger_at);
CREATE INDEX        idx_default_alarm_state_snoozed    ON default_alarm_state(snoozed_to) WHERE snoozed_to IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS default_alarm_state;
