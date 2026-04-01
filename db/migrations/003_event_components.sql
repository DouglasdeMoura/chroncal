-- +goose Up

-- RFC 5545 sub-components for events: VALARM, ATTENDEE, ATTACH, etc.

-- VALARM components.
CREATE TABLE event_alarms (
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
    attach_fmttype TEXT
);

CREATE INDEX idx_event_alarms_event_id ON event_alarms(event_id);
CREATE UNIQUE INDEX idx_event_alarms_uid ON event_alarms(uid) WHERE uid IS NOT NULL;

-- EMAIL alarm attendees (recipients for ACTION:EMAIL).
CREATE TABLE event_alarm_attendees (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id INTEGER NOT NULL REFERENCES event_alarms(id) ON DELETE CASCADE,
    email    TEXT    NOT NULL,
    name     TEXT
);

CREATE INDEX idx_event_alarm_attendees_alarm_id ON event_alarm_attendees(alarm_id);

-- ATTENDEE and ORGANIZER properties.
CREATE TABLE event_attendees (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    email          TEXT    NOT NULL,
    name           TEXT,
    rsvp_status    TEXT    NOT NULL DEFAULT 'NEEDS-ACTION'
        CHECK(rsvp_status IN ('NEEDS-ACTION','ACCEPTED','DECLINED','TENTATIVE','DELEGATED')),
    role           TEXT    NOT NULL DEFAULT 'REQ-PARTICIPANT'
        CHECK(role IN ('CHAIR','REQ-PARTICIPANT','OPT-PARTICIPANT','NON-PARTICIPANT')),
    organizer      INTEGER NOT NULL DEFAULT 0
        CHECK(organizer IN (0, 1)),
    cutype         TEXT
        CHECK(cutype IS NULL OR cutype IN ('INDIVIDUAL','GROUP','RESOURCE','ROOM','UNKNOWN')),
    rsvp           TEXT
        CHECK(rsvp IS NULL OR rsvp IN ('TRUE','FALSE')),
    sent_by        TEXT,
    delegated_to   TEXT,
    delegated_from TEXT,
    member         TEXT,
    dir            TEXT,
    language       TEXT
);

CREATE INDEX idx_event_attendees_event_id ON event_attendees(event_id);

-- ATTACH property.
CREATE TABLE event_attachments (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    uri      TEXT    NOT NULL,
    fmttype  TEXT,
    data     BLOB,
    filename TEXT
);

CREATE INDEX idx_event_attachments_event_id ON event_attachments(event_id);

-- COMMENT property.
CREATE TABLE event_comments (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_comments_event_id ON event_comments(event_id);

-- CONTACT property.
CREATE TABLE event_contacts (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_contacts_event_id ON event_contacts(event_id);

-- RESOURCES property.
CREATE TABLE event_resources (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_resources_event_id ON event_resources(event_id);

-- RELATED-TO property.
CREATE TABLE event_relations (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT'
        CHECK(rel_type IN ('PARENT','CHILD','SIBLING')),
    rel_uid  TEXT
);

CREATE INDEX idx_event_relations_event_id ON event_relations(event_id);

-- +goose Down
DROP TABLE IF EXISTS event_relations;
DROP TABLE IF EXISTS event_resources;
DROP TABLE IF EXISTS event_contacts;
DROP TABLE IF EXISTS event_comments;
DROP TABLE IF EXISTS event_attachments;
DROP TABLE IF EXISTS event_attendees;
DROP TABLE IF EXISTS event_alarm_attendees;
DROP TABLE IF EXISTS event_alarms;
