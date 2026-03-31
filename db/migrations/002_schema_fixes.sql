-- +goose Up

-- Alarm state indexes for time-based queries.
-- ListExpiredSnoozedAlarmStates scans by snoozed_to; the existing unique index
-- (alarm_id, trigger_at) does not help these queries.
CREATE INDEX idx_alarm_state_trigger_at ON alarm_state(trigger_at);
CREATE INDEX idx_todo_alarm_state_trigger_at ON todo_alarm_state(trigger_at);
CREATE INDEX idx_alarm_state_snoozed ON alarm_state(snoozed_to) WHERE snoozed_to IS NOT NULL;
CREATE INDEX idx_todo_alarm_state_snoozed ON todo_alarm_state(snoozed_to) WHERE snoozed_to IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_alarm_state_trigger_at;
DROP INDEX IF EXISTS idx_todo_alarm_state_trigger_at;
DROP INDEX IF EXISTS idx_alarm_state_snoozed;
DROP INDEX IF EXISTS idx_todo_alarm_state_snoozed;
