-- +goose Up

-- RFC 5545 sub-components for todos: VALARM, ATTENDEE, ATTACH, etc.

-- VALARM components.
CREATE TABLE todo_alarms (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id        INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    action         TEXT    NOT NULL DEFAULT 'DISPLAY',
    trigger_value  TEXT    NOT NULL,
    description    TEXT,
    repeat         INTEGER NOT NULL DEFAULT 0,
    duration       TEXT,
    related        TEXT    NOT NULL DEFAULT 'START',
    summary        TEXT,
    uid            TEXT,
    acknowledged   TEXT,
    attach_uri     TEXT,
    attach_fmttype TEXT
);

CREATE INDEX idx_todo_alarms_todo_id ON todo_alarms(todo_id);
CREATE UNIQUE INDEX idx_todo_alarms_uid ON todo_alarms(uid) WHERE uid IS NOT NULL;

-- EMAIL alarm attendees.
CREATE TABLE todo_alarm_attendees (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id INTEGER NOT NULL REFERENCES todo_alarms(id) ON DELETE CASCADE,
    email    TEXT    NOT NULL,
    name     TEXT
);

CREATE INDEX idx_todo_alarm_attendees_alarm_id ON todo_alarm_attendees(alarm_id);

-- ATTENDEE and ORGANIZER properties.
CREATE TABLE todo_attendees (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id        INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    email          TEXT    NOT NULL,
    name           TEXT,
    rsvp_status    TEXT    NOT NULL DEFAULT 'NEEDS-ACTION',
    role           TEXT    NOT NULL DEFAULT 'REQ-PARTICIPANT',
    organizer      INTEGER NOT NULL DEFAULT 0,
    cutype         TEXT,
    rsvp           TEXT,
    sent_by        TEXT,
    delegated_to   TEXT,
    delegated_from TEXT,
    member         TEXT,
    dir            TEXT,
    language       TEXT
);

CREATE INDEX idx_todo_attendees_todo_id ON todo_attendees(todo_id);

-- ATTACH property.
CREATE TABLE todo_attachments (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    uri      TEXT    NOT NULL,
    fmttype  TEXT,
    data     BLOB,
    filename TEXT
);

CREATE INDEX idx_todo_attachments_todo_id ON todo_attachments(todo_id);

-- COMMENT property.
CREATE TABLE todo_comments (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_comments_todo_id ON todo_comments(todo_id);

-- CONTACT property.
CREATE TABLE todo_contacts (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_contacts_todo_id ON todo_contacts(todo_id);

-- RESOURCES property.
CREATE TABLE todo_resources (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_resources_todo_id ON todo_resources(todo_id);

-- RELATED-TO property.
CREATE TABLE todo_relations (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT',
    rel_uid  TEXT
);

CREATE INDEX idx_todo_relations_todo_id ON todo_relations(todo_id);

-- +goose Down
DROP TABLE IF EXISTS todo_relations;
DROP TABLE IF EXISTS todo_resources;
DROP TABLE IF EXISTS todo_contacts;
DROP TABLE IF EXISTS todo_comments;
DROP TABLE IF EXISTS todo_attachments;
DROP TABLE IF EXISTS todo_attendees;
DROP TABLE IF EXISTS todo_alarm_attendees;
DROP TABLE IF EXISTS todo_alarms;
