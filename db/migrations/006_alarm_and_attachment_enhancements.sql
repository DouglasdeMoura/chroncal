-- +goose Up

-- Alarm enhancements: repeat cycle + trigger anchor
ALTER TABLE event_alarms ADD COLUMN repeat INTEGER NOT NULL DEFAULT 0;
ALTER TABLE event_alarms ADD COLUMN duration TEXT NOT NULL DEFAULT '';
ALTER TABLE event_alarms ADD COLUMN related TEXT NOT NULL DEFAULT 'START';

ALTER TABLE todo_alarms ADD COLUMN repeat INTEGER NOT NULL DEFAULT 0;
ALTER TABLE todo_alarms ADD COLUMN duration TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_alarms ADD COLUMN related TEXT NOT NULL DEFAULT 'START';

-- Alarm attendees (EMAIL action recipients)
CREATE TABLE event_alarm_attendees (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id INTEGER NOT NULL REFERENCES event_alarms(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_event_alarm_attendees_alarm_id ON event_alarm_attendees(alarm_id);

CREATE TABLE todo_alarm_attendees (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id INTEGER NOT NULL REFERENCES todo_alarms(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_todo_alarm_attendees_alarm_id ON todo_alarm_attendees(alarm_id);

-- Attachment enhancements: inline binary support
ALTER TABLE event_attachments ADD COLUMN data BLOB DEFAULT NULL;
ALTER TABLE event_attachments ADD COLUMN filename TEXT NOT NULL DEFAULT '';

ALTER TABLE todo_attachments ADD COLUMN data BLOB DEFAULT NULL;
ALTER TABLE todo_attachments ADD COLUMN filename TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE todo_attachments DROP COLUMN filename;
ALTER TABLE todo_attachments DROP COLUMN data;

ALTER TABLE event_attachments DROP COLUMN filename;
ALTER TABLE event_attachments DROP COLUMN data;

DROP TABLE todo_alarm_attendees;
DROP TABLE event_alarm_attendees;

ALTER TABLE todo_alarms DROP COLUMN related;
ALTER TABLE todo_alarms DROP COLUMN duration;
ALTER TABLE todo_alarms DROP COLUMN repeat;

ALTER TABLE event_alarms DROP COLUMN related;
ALTER TABLE event_alarms DROP COLUMN duration;
ALTER TABLE event_alarms DROP COLUMN repeat;
