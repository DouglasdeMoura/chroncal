-- +goose Up
-- RFC 5545 makes UID unique inside one calendar, not across calendars.
-- UNIQUE(uid, recurrence_id) made a second CalDAV collection's copy of the
-- same meeting overwrite the first (issue #756). Scope uniqueness to
-- (calendar_id, uid, recurrence_id). SQLite cannot drop a table UNIQUE
-- constraint, so rebuild the events table.
--
-- Foreign keys are ON. DROP TABLE events deletes child rows through
-- ON DELETE CASCADE. Back up each child table first. Restore it after
-- the rebuild. Drop FTS and x_properties DELETE triggers first so a
-- cascade does not rewrite events_fts or erase x_properties rows.

DROP TRIGGER IF EXISTS events_fts_ai;
DROP TRIGGER IF EXISTS events_fts_au;
DROP TRIGGER IF EXISTS events_fts_bd;
DROP TRIGGER IF EXISTS events_x_properties_ad;
DROP TRIGGER IF EXISTS event_alarms_x_properties_ad;
DROP TRIGGER IF EXISTS x_properties_event_owner_exists;
DROP TRIGGER IF EXISTS event_categories_fts_ai;
DROP TRIGGER IF EXISTS event_categories_fts_ad;

CREATE TABLE event_categories_backup AS SELECT * FROM event_categories;
CREATE TABLE event_alarms_backup AS SELECT * FROM event_alarms;
CREATE TABLE event_alarm_attendees_backup AS SELECT * FROM event_alarm_attendees;
CREATE TABLE alarm_state_backup AS SELECT * FROM alarm_state;
CREATE TABLE event_attendees_backup AS SELECT * FROM event_attendees;
CREATE TABLE event_attachments_backup AS SELECT * FROM event_attachments;
CREATE TABLE event_comments_backup AS SELECT * FROM event_comments;
CREATE TABLE event_contacts_backup AS SELECT * FROM event_contacts;
CREATE TABLE event_resources_backup AS SELECT * FROM event_resources;
CREATE TABLE event_relations_backup AS SELECT * FROM event_relations;

CREATE TABLE events_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    uid             TEXT    NOT NULL,
    calendar_id     INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    description     TEXT,
    location        TEXT,
    start_time      TEXT    NOT NULL,
    end_time        TEXT    NOT NULL,
    all_day         INTEGER NOT NULL DEFAULT 0
        CHECK(all_day IN (0, 1)),
    recurrence_rule TEXT,
    timezone        TEXT,
    status          TEXT    NOT NULL DEFAULT 'CONFIRMED'
        CHECK(status IN ('TENTATIVE','CONFIRMED','CANCELLED')),
    transp          TEXT    NOT NULL DEFAULT 'OPAQUE'
        CHECK(transp IN ('OPAQUE','TRANSPARENT')),
    sequence        INTEGER NOT NULL DEFAULT 0
        CHECK(sequence >= 0),
    priority        INTEGER NOT NULL DEFAULT 0
        CHECK(priority BETWEEN 0 AND 9),
    class           TEXT    NOT NULL DEFAULT 'PUBLIC'
        CHECK(class IN ('PUBLIC','PRIVATE','CONFIDENTIAL')),
    url             TEXT,
    exdates         TEXT,
    rdates          TEXT,
    recurrence_id   TEXT    NOT NULL DEFAULT '',
    geo             TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    duration        TEXT,
    dtstamp         TEXT,
    conference_uri  TEXT    NOT NULL DEFAULT '',
    deleted_at      TEXT,
    UNIQUE(calendar_id, uid, recurrence_id)
);

INSERT INTO events_new (
    id, uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule, timezone,
    status, transp, sequence, priority, class, url, exdates, rdates,
    recurrence_id, geo, created_at, updated_at, duration, dtstamp,
    conference_uri, deleted_at
)
SELECT
    id, uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule, timezone,
    status, transp, sequence, priority, class, url, exdates, rdates,
    recurrence_id, geo, created_at, updated_at, duration, dtstamp,
    conference_uri, deleted_at
FROM events;

DROP TABLE events;
ALTER TABLE events_new RENAME TO events;

CREATE INDEX idx_events_cal_start  ON events(calendar_id, start_time);
CREATE INDEX idx_events_start_time ON events(start_time);
CREATE INDEX idx_events_end_time   ON events(end_time);
CREATE INDEX idx_events_deleted_at ON events(deleted_at) WHERE deleted_at IS NOT NULL;

INSERT INTO event_categories SELECT * FROM event_categories_backup;
INSERT INTO event_alarms SELECT * FROM event_alarms_backup;
INSERT INTO event_alarm_attendees SELECT * FROM event_alarm_attendees_backup;
INSERT INTO alarm_state SELECT * FROM alarm_state_backup;
INSERT INTO event_attendees SELECT * FROM event_attendees_backup;
INSERT INTO event_attachments SELECT * FROM event_attachments_backup;
INSERT INTO event_comments SELECT * FROM event_comments_backup;
INSERT INTO event_contacts SELECT * FROM event_contacts_backup;
INSERT INTO event_resources SELECT * FROM event_resources_backup;
INSERT INTO event_relations SELECT * FROM event_relations_backup;

DROP TABLE event_categories_backup;
DROP TABLE event_alarms_backup;
DROP TABLE event_alarm_attendees_backup;
DROP TABLE alarm_state_backup;
DROP TABLE event_attendees_backup;
DROP TABLE event_attachments_backup;
DROP TABLE event_comments_backup;
DROP TABLE event_contacts_backup;
DROP TABLE event_resources_backup;
DROP TABLE event_relations_backup;

-- +goose StatementBegin
CREATE TRIGGER events_fts_ai AFTER INSERT ON events BEGIN
    INSERT INTO events_fts(rowid, title, description, location, categories)
    VALUES (NEW.id, NEW.title, COALESCE(NEW.description, ''), COALESCE(NEW.location, ''), '');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_fts_au AFTER UPDATE ON events BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    VALUES (NEW.id, NEW.title, COALESCE(NEW.description, ''), COALESCE(NEW.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = NEW.id), ''));
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_fts_bd BEFORE DELETE ON events BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER event_categories_fts_ai AFTER INSERT ON event_categories BEGIN
    DELETE FROM events_fts WHERE rowid = NEW.event_id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = e.id), '')
    FROM events e WHERE e.id = NEW.event_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER event_categories_fts_ad AFTER DELETE ON event_categories
WHEN EXISTS (SELECT 1 FROM events WHERE id = OLD.event_id) BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.event_id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = e.id), '')
    FROM events e WHERE e.id = OLD.event_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_x_properties_ad
AFTER DELETE ON events BEGIN
    DELETE FROM x_properties WHERE owner_type = 'event' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER x_properties_event_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'event'
 AND NOT EXISTS (SELECT 1 FROM events WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER event_alarms_x_properties_ad
AFTER DELETE ON event_alarms BEGIN
    DELETE FROM x_properties WHERE owner_type = 'event_alarm' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS events_fts_ai;
DROP TRIGGER IF EXISTS events_fts_au;
DROP TRIGGER IF EXISTS events_fts_bd;
DROP TRIGGER IF EXISTS events_x_properties_ad;
DROP TRIGGER IF EXISTS event_alarms_x_properties_ad;
DROP TRIGGER IF EXISTS x_properties_event_owner_exists;
DROP TRIGGER IF EXISTS event_categories_fts_ai;
DROP TRIGGER IF EXISTS event_categories_fts_ad;

CREATE TABLE event_categories_backup AS SELECT * FROM event_categories;
CREATE TABLE event_alarms_backup AS SELECT * FROM event_alarms;
CREATE TABLE event_alarm_attendees_backup AS SELECT * FROM event_alarm_attendees;
CREATE TABLE alarm_state_backup AS SELECT * FROM alarm_state;
CREATE TABLE event_attendees_backup AS SELECT * FROM event_attendees;
CREATE TABLE event_attachments_backup AS SELECT * FROM event_attachments;
CREATE TABLE event_comments_backup AS SELECT * FROM event_comments;
CREATE TABLE event_contacts_backup AS SELECT * FROM event_contacts;
CREATE TABLE event_resources_backup AS SELECT * FROM event_resources;
CREATE TABLE event_relations_backup AS SELECT * FROM event_relations;

CREATE TABLE events_old (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    uid             TEXT    NOT NULL,
    calendar_id     INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    description     TEXT,
    location        TEXT,
    start_time      TEXT    NOT NULL,
    end_time        TEXT    NOT NULL,
    all_day         INTEGER NOT NULL DEFAULT 0
        CHECK(all_day IN (0, 1)),
    recurrence_rule TEXT,
    timezone        TEXT,
    status          TEXT    NOT NULL DEFAULT 'CONFIRMED'
        CHECK(status IN ('TENTATIVE','CONFIRMED','CANCELLED')),
    transp          TEXT    NOT NULL DEFAULT 'OPAQUE'
        CHECK(transp IN ('OPAQUE','TRANSPARENT')),
    sequence        INTEGER NOT NULL DEFAULT 0
        CHECK(sequence >= 0),
    priority        INTEGER NOT NULL DEFAULT 0
        CHECK(priority BETWEEN 0 AND 9),
    class           TEXT    NOT NULL DEFAULT 'PUBLIC'
        CHECK(class IN ('PUBLIC','PRIVATE','CONFIDENTIAL')),
    url             TEXT,
    exdates         TEXT,
    rdates          TEXT,
    recurrence_id   TEXT    NOT NULL DEFAULT '',
    geo             TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    duration        TEXT,
    dtstamp         TEXT,
    conference_uri  TEXT    NOT NULL DEFAULT '',
    deleted_at      TEXT,
    UNIQUE(uid, recurrence_id)
);

INSERT INTO events_old (
    id, uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule, timezone,
    status, transp, sequence, priority, class, url, exdates, rdates,
    recurrence_id, geo, created_at, updated_at, duration, dtstamp,
    conference_uri, deleted_at
)
SELECT
    id, uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule, timezone,
    status, transp, sequence, priority, class, url, exdates, rdates,
    recurrence_id, geo, created_at, updated_at, duration, dtstamp,
    conference_uri, deleted_at
FROM events;

DROP TABLE events;
ALTER TABLE events_old RENAME TO events;

CREATE INDEX idx_events_cal_start  ON events(calendar_id, start_time);
CREATE INDEX idx_events_start_time ON events(start_time);
CREATE INDEX idx_events_end_time   ON events(end_time);
CREATE INDEX idx_events_deleted_at ON events(deleted_at) WHERE deleted_at IS NOT NULL;

INSERT INTO event_categories SELECT * FROM event_categories_backup;
INSERT INTO event_alarms SELECT * FROM event_alarms_backup;
INSERT INTO event_alarm_attendees SELECT * FROM event_alarm_attendees_backup;
INSERT INTO alarm_state SELECT * FROM alarm_state_backup;
INSERT INTO event_attendees SELECT * FROM event_attendees_backup;
INSERT INTO event_attachments SELECT * FROM event_attachments_backup;
INSERT INTO event_comments SELECT * FROM event_comments_backup;
INSERT INTO event_contacts SELECT * FROM event_contacts_backup;
INSERT INTO event_resources SELECT * FROM event_resources_backup;
INSERT INTO event_relations SELECT * FROM event_relations_backup;

DROP TABLE event_categories_backup;
DROP TABLE event_alarms_backup;
DROP TABLE event_alarm_attendees_backup;
DROP TABLE alarm_state_backup;
DROP TABLE event_attendees_backup;
DROP TABLE event_attachments_backup;
DROP TABLE event_comments_backup;
DROP TABLE event_contacts_backup;
DROP TABLE event_resources_backup;
DROP TABLE event_relations_backup;

-- +goose StatementBegin
CREATE TRIGGER events_fts_ai AFTER INSERT ON events BEGIN
    INSERT INTO events_fts(rowid, title, description, location, categories)
    VALUES (NEW.id, NEW.title, COALESCE(NEW.description, ''), COALESCE(NEW.location, ''), '');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_fts_au AFTER UPDATE ON events BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    VALUES (NEW.id, NEW.title, COALESCE(NEW.description, ''), COALESCE(NEW.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = NEW.id), ''));
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_fts_bd BEFORE DELETE ON events BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER event_categories_fts_ai AFTER INSERT ON event_categories BEGIN
    DELETE FROM events_fts WHERE rowid = NEW.event_id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = e.id), '')
    FROM events e WHERE e.id = NEW.event_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER event_categories_fts_ad AFTER DELETE ON event_categories
WHEN EXISTS (SELECT 1 FROM events WHERE id = OLD.event_id) BEGIN
    DELETE FROM events_fts WHERE rowid = OLD.event_id;
    INSERT INTO events_fts(rowid, title, description, location, categories)
    SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
        COALESCE((SELECT GROUP_CONCAT(ec.category, ' ')
                  FROM event_categories ec WHERE ec.event_id = e.id), '')
    FROM events e WHERE e.id = OLD.event_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER events_x_properties_ad
AFTER DELETE ON events BEGIN
    DELETE FROM x_properties WHERE owner_type = 'event' AND owner_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER x_properties_event_owner_exists
BEFORE INSERT ON x_properties
WHEN NEW.owner_type = 'event'
 AND NOT EXISTS (SELECT 1 FROM events WHERE id = NEW.owner_id) BEGIN
    SELECT RAISE(ABORT, 'x_properties owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER event_alarms_x_properties_ad
AFTER DELETE ON event_alarms BEGIN
    DELETE FROM x_properties WHERE owner_type = 'event_alarm' AND owner_id = OLD.id;
END;
-- +goose StatementEnd
