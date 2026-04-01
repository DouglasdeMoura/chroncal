-- +goose Up

-- Alarm state indexes for time-based queries.
-- ListExpiredSnoozedAlarmStates scans by snoozed_to; the existing unique index
-- (alarm_id, trigger_at) does not help these queries.
CREATE INDEX idx_alarm_state_trigger_at ON alarm_state(trigger_at);
CREATE INDEX idx_todo_alarm_state_trigger_at ON todo_alarm_state(trigger_at);
CREATE INDEX idx_alarm_state_snoozed ON alarm_state(snoozed_to) WHERE snoozed_to IS NOT NULL;
CREATE INDEX idx_todo_alarm_state_snoozed ON todo_alarm_state(snoozed_to) WHERE snoozed_to IS NOT NULL;

-- Event duration column for round-trip fidelity.
-- Events with DURATION are converted to DTEND on import; this column preserves
-- the original RFC 5545 DURATION string (e.g. "PT1H") so it can be re-emitted.
ALTER TABLE events ADD COLUMN duration TEXT;

-- DTSTAMP column for RFC 5545 DTSTAMP/LAST-MODIFIED distinction.
-- DTSTAMP reflects when the iCalendar object was created/sent; LAST-MODIFIED
-- reflects when the component was last changed. Previously both mapped to updated_at.
ALTER TABLE events ADD COLUMN dtstamp TEXT;
ALTER TABLE todos ADD COLUMN dtstamp TEXT;

-- Timezones table for VTIMEZONE round-tripping.
-- Stores raw VTIMEZONE component data from imports so non-standard or custom
-- timezone definitions can be faithfully re-exported.
CREATE TABLE timezones (
    tzid           TEXT PRIMARY KEY,
    vtimezone_data TEXT NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- +goose Down
DROP TABLE IF EXISTS timezones;
ALTER TABLE todos DROP COLUMN dtstamp;
ALTER TABLE events DROP COLUMN dtstamp;
ALTER TABLE events DROP COLUMN duration;
DROP INDEX IF EXISTS idx_alarm_state_trigger_at;
DROP INDEX IF EXISTS idx_todo_alarm_state_trigger_at;
DROP INDEX IF EXISTS idx_alarm_state_snoozed;
DROP INDEX IF EXISTS idx_todo_alarm_state_snoozed;
