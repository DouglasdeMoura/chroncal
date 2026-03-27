-- +goose Up

-- Rebuild events table: relax UID unique constraint to UNIQUE(uid, recurrence_id)
-- and add RFC 5545 columns. SQLite requires table rebuild to change constraints.
CREATE TABLE events_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    uid             TEXT    NOT NULL,
    calendar_id     INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    location        TEXT    NOT NULL DEFAULT '',
    start_time      TEXT    NOT NULL,
    end_time        TEXT    NOT NULL,
    all_day         INTEGER NOT NULL DEFAULT 0,
    recurrence_rule TEXT    NOT NULL DEFAULT '',
    timezone        TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'CONFIRMED',
    transp          TEXT    NOT NULL DEFAULT 'OPAQUE',
    sequence        INTEGER NOT NULL DEFAULT 0,
    priority        INTEGER NOT NULL DEFAULT 0,
    class           TEXT    NOT NULL DEFAULT 'PUBLIC',
    url             TEXT    NOT NULL DEFAULT '',
    categories      TEXT    NOT NULL DEFAULT '',
    exdates         TEXT    NOT NULL DEFAULT '',
    rdates          TEXT    NOT NULL DEFAULT '',
    recurrence_id   TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(uid, recurrence_id)
);

INSERT INTO events_new (id, uid, calendar_id, title, description, location, start_time, end_time, all_day, recurrence_rule, created_at, updated_at)
SELECT id, uid, calendar_id, title, description, location, start_time, end_time, all_day, recurrence_rule, created_at, updated_at
FROM events;

DROP TABLE events;
ALTER TABLE events_new RENAME TO events;

CREATE INDEX idx_events_calendar_id ON events(calendar_id);
CREATE INDEX idx_events_start_time ON events(start_time);
CREATE INDEX idx_events_uid ON events(uid);
CREATE INDEX idx_events_recurrence ON events(uid, recurrence_id);

-- Alarms (VALARM components)
CREATE TABLE event_alarms (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id      INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    action        TEXT    NOT NULL DEFAULT 'DISPLAY',
    trigger_value TEXT    NOT NULL,
    description   TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_event_alarms_event_id ON event_alarms(event_id);

-- Attendees (ATTENDEE + ORGANIZER properties)
CREATE TABLE event_attendees (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    email       TEXT    NOT NULL,
    name        TEXT    NOT NULL DEFAULT '',
    rsvp_status TEXT    NOT NULL DEFAULT 'NEEDS-ACTION',
    role        TEXT    NOT NULL DEFAULT 'REQ-PARTICIPANT',
    organizer   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_event_attendees_event_id ON event_attendees(event_id);

-- +goose Down
DROP TABLE event_attendees;
DROP TABLE event_alarms;

CREATE TABLE events_old (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    uid             TEXT    NOT NULL UNIQUE,
    calendar_id     INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    location        TEXT    NOT NULL DEFAULT '',
    start_time      TEXT    NOT NULL,
    end_time        TEXT    NOT NULL,
    all_day         INTEGER NOT NULL DEFAULT 0,
    recurrence_rule TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

INSERT INTO events_old (id, uid, calendar_id, title, description, location, start_time, end_time, all_day, recurrence_rule, created_at, updated_at)
SELECT id, uid, calendar_id, title, description, location, start_time, end_time, all_day, recurrence_rule, created_at, updated_at
FROM events;

DROP TABLE events;
ALTER TABLE events_old RENAME TO events;

CREATE INDEX idx_events_calendar_id ON events(calendar_id);
CREATE INDEX idx_events_start_time ON events(start_time);
CREATE INDEX idx_events_uid ON events(uid);
