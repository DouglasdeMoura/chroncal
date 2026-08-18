-- +goose Up

-- Widen the action CHECK on event_alarms and todo_alarms to any non-empty
-- value (issue #579). The old constraint permitted only AUDIO, DISPLAY, and
-- EMAIL. Import then dropped a VALARM with an RFC 5545 x-name or iana-token
-- action (for example X-APPLE-SOUND) or with the Google ACTION:NONE
-- sentinel. Every later push deleted that VALARM from the server copy. The
-- alarm engine still fires only the three actions in
-- model.FireableAlarmAction. The wide constraint mirrors
-- model.StorableAlarmAction.
--
-- SQLite cannot alter a CHECK constraint, so rebuild both tables (the
-- migration 031 pattern). Two extra hazards apply here:
--
-- 1. Foreign keys are ON. DROP TABLE performs an implicit DELETE, and the
--    ON DELETE CASCADE clauses in alarm_state, todo_alarm_state,
--    event_alarm_attendees, and todo_alarm_attendees then erase those
--    rows. Back up each child table first. Restore it after the rebuild.
--    The implicit DELETE fires no SQL triggers, so the alarm-owned
--    x_properties rows survive in place.
-- 2. The x_properties owner-exists triggers reference the alarm tables by
--    name. The rename reparses the schema and fails on a trigger that
--    points at a missing table. Drop those triggers first. Recreate
--    them after. The DROP also removes the AFTER DELETE cleanup triggers
--    on the alarm tables; recreate them too.
DROP TRIGGER IF EXISTS x_properties_event_alarm_owner_exists;
DROP TRIGGER IF EXISTS x_properties_todo_alarm_owner_exists;

CREATE TABLE alarm_state_backup AS SELECT * FROM alarm_state;
CREATE TABLE event_alarm_attendees_backup AS SELECT * FROM event_alarm_attendees;
CREATE TABLE todo_alarm_state_backup AS SELECT * FROM todo_alarm_state;
CREATE TABLE todo_alarm_attendees_backup AS SELECT * FROM todo_alarm_attendees;

CREATE TABLE event_alarms_new (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    action         TEXT    NOT NULL DEFAULT 'DISPLAY'
        CHECK(action <> ''),
    trigger_value  TEXT    NOT NULL,
    description    TEXT,
    repeat         INTEGER NOT NULL DEFAULT 0,
    duration       TEXT,
    related        TEXT    NOT NULL DEFAULT 'START'
        CHECK(related IN ('START','END')),
    summary        TEXT,
    uid            TEXT,
    acknowledged   TEXT,
    attach_uri     TEXT,
    attach_fmttype TEXT,
    attach_binary  BLOB
);

INSERT INTO event_alarms_new (id, event_id, action, trigger_value, description,
    repeat, duration, related, summary, uid, acknowledged, attach_uri,
    attach_fmttype, attach_binary)
SELECT id, event_id, action, trigger_value, description,
    repeat, duration, related, summary, uid, acknowledged, attach_uri,
    attach_fmttype, attach_binary
FROM event_alarms;

DROP TABLE event_alarms;
ALTER TABLE event_alarms_new RENAME TO event_alarms;

CREATE INDEX idx_event_alarms_event_id ON event_alarms(event_id);
CREATE UNIQUE INDEX idx_event_alarms_uid ON event_alarms(uid) WHERE uid IS NOT NULL;

CREATE TABLE todo_alarms_new (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id        INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    action         TEXT    NOT NULL DEFAULT 'DISPLAY'
        CHECK(action <> ''),
    trigger_value  TEXT    NOT NULL,
    description    TEXT,
    repeat         INTEGER NOT NULL DEFAULT 0,
    duration       TEXT,
    related        TEXT    NOT NULL DEFAULT 'START'
        CHECK(related IN ('START','END')),
    summary        TEXT,
    uid            TEXT,
    acknowledged   TEXT,
    attach_uri     TEXT,
    attach_fmttype TEXT,
    attach_binary  BLOB
);

INSERT INTO todo_alarms_new (id, todo_id, action, trigger_value, description,
    repeat, duration, related, summary, uid, acknowledged, attach_uri,
    attach_fmttype, attach_binary)
SELECT id, todo_id, action, trigger_value, description,
    repeat, duration, related, summary, uid, acknowledged, attach_uri,
    attach_fmttype, attach_binary
FROM todo_alarms;

DROP TABLE todo_alarms;
ALTER TABLE todo_alarms_new RENAME TO todo_alarms;

CREATE INDEX idx_todo_alarms_todo_id ON todo_alarms(todo_id);
CREATE UNIQUE INDEX idx_todo_alarms_uid ON todo_alarms(uid) WHERE uid IS NOT NULL;

-- Restore the child rows the implicit DELETE cascaded away. The DELETE
-- before each restore makes the step idempotent when no cascade ran.
DELETE FROM alarm_state;
INSERT INTO alarm_state SELECT * FROM alarm_state_backup;
DROP TABLE alarm_state_backup;

DELETE FROM event_alarm_attendees;
INSERT INTO event_alarm_attendees SELECT * FROM event_alarm_attendees_backup;
DROP TABLE event_alarm_attendees_backup;

DELETE FROM todo_alarm_state;
INSERT INTO todo_alarm_state SELECT * FROM todo_alarm_state_backup;
DROP TABLE todo_alarm_state_backup;

DELETE FROM todo_alarm_attendees;
INSERT INTO todo_alarm_attendees SELECT * FROM todo_alarm_attendees_backup;
DROP TABLE todo_alarm_attendees_backup;

-- +goose StatementBegin
CREATE TRIGGER x_properties_event_alarm_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'event_alarm'
 AND NOT EXISTS (SELECT 1 FROM event_alarms WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER x_properties_todo_alarm_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'todo_alarm'
 AND NOT EXISTS (SELECT 1 FROM todo_alarms WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER event_alarms_x_properties_ad
AFTER DELETE ON event_alarms BEGIN
    DELETE FROM x_properties WHERE owner_type = 'event_alarm' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER todo_alarms_x_properties_ad
AFTER DELETE ON todo_alarms BEGIN
    DELETE FROM x_properties WHERE owner_type = 'todo_alarm' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose Down

-- Intentional, unrecoverable data loss: the narrow CHECK cannot hold an
-- alarm with an action outside AUDIO, DISPLAY, and EMAIL. Delete those
-- alarms first. The DELETE cascades their state and attendee rows and the
-- AFTER DELETE trigger removes their x_properties rows. The loss is not
-- local only. The next push omits the deleted VALARMs, so the server copy
-- loses them too, and Google re-applies the default reminders that its
-- ACTION:NONE sentinel turned off.
DELETE FROM event_alarms WHERE action NOT IN ('AUDIO','DISPLAY','EMAIL');
DELETE FROM todo_alarms WHERE action NOT IN ('AUDIO','DISPLAY','EMAIL');

DROP TRIGGER IF EXISTS x_properties_event_alarm_owner_exists;
DROP TRIGGER IF EXISTS x_properties_todo_alarm_owner_exists;

CREATE TABLE alarm_state_backup AS SELECT * FROM alarm_state;
CREATE TABLE event_alarm_attendees_backup AS SELECT * FROM event_alarm_attendees;
CREATE TABLE todo_alarm_state_backup AS SELECT * FROM todo_alarm_state;
CREATE TABLE todo_alarm_attendees_backup AS SELECT * FROM todo_alarm_attendees;

CREATE TABLE event_alarms_old (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    action         TEXT    NOT NULL DEFAULT 'DISPLAY'
        CHECK(action IN ('AUDIO','DISPLAY','EMAIL')),
    trigger_value  TEXT    NOT NULL,
    description    TEXT,
    repeat         INTEGER NOT NULL DEFAULT 0,
    duration       TEXT,
    related        TEXT    NOT NULL DEFAULT 'START'
        CHECK(related IN ('START','END')),
    summary        TEXT,
    uid            TEXT,
    acknowledged   TEXT,
    attach_uri     TEXT,
    attach_fmttype TEXT,
    attach_binary  BLOB
);

INSERT INTO event_alarms_old (id, event_id, action, trigger_value, description,
    repeat, duration, related, summary, uid, acknowledged, attach_uri,
    attach_fmttype, attach_binary)
SELECT id, event_id, action, trigger_value, description,
    repeat, duration, related, summary, uid, acknowledged, attach_uri,
    attach_fmttype, attach_binary
FROM event_alarms;

DROP TABLE event_alarms;
ALTER TABLE event_alarms_old RENAME TO event_alarms;

CREATE INDEX idx_event_alarms_event_id ON event_alarms(event_id);
CREATE UNIQUE INDEX idx_event_alarms_uid ON event_alarms(uid) WHERE uid IS NOT NULL;

CREATE TABLE todo_alarms_old (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id        INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    action         TEXT    NOT NULL DEFAULT 'DISPLAY'
        CHECK(action IN ('AUDIO','DISPLAY','EMAIL')),
    trigger_value  TEXT    NOT NULL,
    description    TEXT,
    repeat         INTEGER NOT NULL DEFAULT 0,
    duration       TEXT,
    related        TEXT    NOT NULL DEFAULT 'START'
        CHECK(related IN ('START','END')),
    summary        TEXT,
    uid            TEXT,
    acknowledged   TEXT,
    attach_uri     TEXT,
    attach_fmttype TEXT,
    attach_binary  BLOB
);

INSERT INTO todo_alarms_old (id, todo_id, action, trigger_value, description,
    repeat, duration, related, summary, uid, acknowledged, attach_uri,
    attach_fmttype, attach_binary)
SELECT id, todo_id, action, trigger_value, description,
    repeat, duration, related, summary, uid, acknowledged, attach_uri,
    attach_fmttype, attach_binary
FROM todo_alarms;

DROP TABLE todo_alarms;
ALTER TABLE todo_alarms_old RENAME TO todo_alarms;

CREATE INDEX idx_todo_alarms_todo_id ON todo_alarms(todo_id);
CREATE UNIQUE INDEX idx_todo_alarms_uid ON todo_alarms(uid) WHERE uid IS NOT NULL;

DELETE FROM alarm_state;
INSERT INTO alarm_state SELECT * FROM alarm_state_backup;
DROP TABLE alarm_state_backup;

DELETE FROM event_alarm_attendees;
INSERT INTO event_alarm_attendees SELECT * FROM event_alarm_attendees_backup;
DROP TABLE event_alarm_attendees_backup;

DELETE FROM todo_alarm_state;
INSERT INTO todo_alarm_state SELECT * FROM todo_alarm_state_backup;
DROP TABLE todo_alarm_state_backup;

DELETE FROM todo_alarm_attendees;
INSERT INTO todo_alarm_attendees SELECT * FROM todo_alarm_attendees_backup;
DROP TABLE todo_alarm_attendees_backup;

-- +goose StatementBegin
CREATE TRIGGER x_properties_event_alarm_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'event_alarm'
 AND NOT EXISTS (SELECT 1 FROM event_alarms WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER x_properties_todo_alarm_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'todo_alarm'
 AND NOT EXISTS (SELECT 1 FROM todo_alarms WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER event_alarms_x_properties_ad
AFTER DELETE ON event_alarms BEGIN
    DELETE FROM x_properties WHERE owner_type = 'event_alarm' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER todo_alarms_x_properties_ad
AFTER DELETE ON todo_alarms BEGIN
    DELETE FROM x_properties WHERE owner_type = 'todo_alarm' AND owner_id = OLD.id;
END;
-- +goose StatementEnd
