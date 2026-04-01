-- +goose Up

-- Calendars
CREATE TABLE calendars (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    color       TEXT    NOT NULL DEFAULT '#7C3AED',
    description TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

INSERT INTO calendars (name) VALUES ('Personal');

-- Events
CREATE TABLE events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    uid             TEXT    NOT NULL,
    calendar_id     INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    description     TEXT,
    location        TEXT,
    start_time      TEXT    NOT NULL,
    end_time        TEXT    NOT NULL,
    all_day         INTEGER NOT NULL DEFAULT 0,
    recurrence_rule TEXT,
    timezone        TEXT,
    status          TEXT    NOT NULL DEFAULT 'CONFIRMED',
    transp          TEXT    NOT NULL DEFAULT 'OPAQUE',
    sequence        INTEGER NOT NULL DEFAULT 0,
    priority        INTEGER NOT NULL DEFAULT 0,
    class           TEXT    NOT NULL DEFAULT 'PUBLIC',
    url             TEXT,
    exdates         TEXT,
    rdates          TEXT,
    recurrence_id   TEXT    NOT NULL DEFAULT '',
    geo             TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(uid, recurrence_id)
);

CREATE INDEX idx_events_calendar_id ON events(calendar_id);
CREATE INDEX idx_events_start_time  ON events(start_time);
CREATE INDEX idx_events_uid         ON events(uid);
CREATE INDEX idx_events_recurrence  ON events(uid, recurrence_id);

-- Event categories (normalized junction table)
CREATE TABLE event_categories (
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    category TEXT    NOT NULL,
    PRIMARY KEY (event_id, category)
);
CREATE INDEX idx_event_categories_category ON event_categories(category);

-- Event alarms (VALARM)
CREATE TABLE event_alarms (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id      INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    action        TEXT    NOT NULL DEFAULT 'DISPLAY',
    trigger_value TEXT    NOT NULL,
    description   TEXT,
    repeat        INTEGER NOT NULL DEFAULT 0,
    duration      TEXT,
    related       TEXT    NOT NULL DEFAULT 'START',
    summary       TEXT,
    uid           TEXT,
    acknowledged  TEXT,
    attach_uri    TEXT,
    attach_fmttype TEXT
);

CREATE INDEX idx_event_alarms_event_id ON event_alarms(event_id);
CREATE UNIQUE INDEX idx_event_alarms_uid ON event_alarms(uid) WHERE uid IS NOT NULL;

-- Event alarm attendees (for EMAIL action)
CREATE TABLE event_alarm_attendees (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id INTEGER NOT NULL REFERENCES event_alarms(id) ON DELETE CASCADE,
    email    TEXT    NOT NULL,
    name     TEXT
);

CREATE INDEX idx_event_alarm_attendees_alarm_id ON event_alarm_attendees(alarm_id);

-- Event attendees (ATTENDEE + ORGANIZER)
CREATE TABLE event_attendees (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
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

CREATE INDEX idx_event_attendees_event_id ON event_attendees(event_id);

-- Event attachments
CREATE TABLE event_attachments (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    uri      TEXT    NOT NULL,
    fmttype  TEXT,
    data     BLOB,
    filename TEXT
);

CREATE INDEX idx_event_attachments_event_id ON event_attachments(event_id);

-- Event comments
CREATE TABLE event_comments (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_comments_event_id ON event_comments(event_id);

-- Event contacts
CREATE TABLE event_contacts (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_contacts_event_id ON event_contacts(event_id);

-- Event resources
CREATE TABLE event_resources (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL
);

CREATE INDEX idx_event_resources_event_id ON event_resources(event_id);

-- Event relations
CREATE TABLE event_relations (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT',
    rel_uid  TEXT
);

CREATE INDEX idx_event_relations_event_id ON event_relations(event_id);

-- Event alarm state
CREATE TABLE alarm_state (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id   INTEGER NOT NULL REFERENCES event_alarms(id) ON DELETE CASCADE,
    event_id   INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    trigger_at TEXT    NOT NULL,
    fired_at   TEXT,
    acked_at   TEXT,
    snoozed_to TEXT
);

CREATE UNIQUE INDEX idx_alarm_state_unique   ON alarm_state(alarm_id, trigger_at);
CREATE INDEX        idx_alarm_state_event_id ON alarm_state(event_id);

-- Event recurrence cache
CREATE TABLE recurrence_instances (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    original_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    instance_at TEXT    NOT NULL,
    is_override INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE UNIQUE INDEX idx_recurrence_unique      ON recurrence_instances(event_id, instance_at);
CREATE INDEX        idx_recurrence_event        ON recurrence_instances(event_id);
CREATE INDEX        idx_recurrence_instance_at  ON recurrence_instances(instance_at);
CREATE INDEX        idx_recurrence_original     ON recurrence_instances(original_id);

---------------------------------------------------------------------------
-- Todos
---------------------------------------------------------------------------

CREATE TABLE todos (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    uid              TEXT    NOT NULL,
    calendar_id      INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    summary          TEXT    NOT NULL,
    description      TEXT,
    location         TEXT,
    due_date         TEXT,
    start_date       TEXT,
    duration         TEXT,
    completed_at     TEXT,
    percent_complete INTEGER NOT NULL DEFAULT 0,
    status           TEXT    NOT NULL DEFAULT 'NEEDS-ACTION',
    priority         INTEGER NOT NULL DEFAULT 0,
    class            TEXT    NOT NULL DEFAULT 'PUBLIC',
    url              TEXT,
    recurrence_rule  TEXT,
    timezone         TEXT,
    sequence         INTEGER NOT NULL DEFAULT 0,
    exdates          TEXT,
    rdates           TEXT,
    recurrence_id    TEXT    NOT NULL DEFAULT '',
    geo              TEXT,
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(uid, recurrence_id)
);

CREATE INDEX idx_todos_calendar_id ON todos(calendar_id);
CREATE INDEX idx_todos_due_date    ON todos(due_date);
CREATE INDEX idx_todos_status      ON todos(status);
CREATE INDEX idx_todos_uid         ON todos(uid);
CREATE INDEX idx_todos_recurrence  ON todos(uid, recurrence_id);

-- Todo categories (normalized junction table)
CREATE TABLE todo_categories (
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    category TEXT    NOT NULL,
    PRIMARY KEY (todo_id, category)
);
CREATE INDEX idx_todo_categories_category ON todo_categories(category);

-- Todo alarms
CREATE TABLE todo_alarms (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id       INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    action        TEXT    NOT NULL DEFAULT 'DISPLAY',
    trigger_value TEXT    NOT NULL,
    description   TEXT,
    repeat        INTEGER NOT NULL DEFAULT 0,
    duration      TEXT,
    related       TEXT    NOT NULL DEFAULT 'START',
    summary       TEXT,
    uid           TEXT,
    acknowledged  TEXT,
    attach_uri    TEXT,
    attach_fmttype TEXT
);

CREATE INDEX idx_todo_alarms_todo_id ON todo_alarms(todo_id);
CREATE UNIQUE INDEX idx_todo_alarms_uid ON todo_alarms(uid) WHERE uid IS NOT NULL;

-- Todo alarm attendees
CREATE TABLE todo_alarm_attendees (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id INTEGER NOT NULL REFERENCES todo_alarms(id) ON DELETE CASCADE,
    email    TEXT    NOT NULL,
    name     TEXT
);

CREATE INDEX idx_todo_alarm_attendees_alarm_id ON todo_alarm_attendees(alarm_id);

-- Todo attendees
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

-- Todo attachments
CREATE TABLE todo_attachments (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    uri      TEXT    NOT NULL,
    fmttype  TEXT,
    data     BLOB,
    filename TEXT
);

CREATE INDEX idx_todo_attachments_todo_id ON todo_attachments(todo_id);

-- Todo comments
CREATE TABLE todo_comments (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_comments_todo_id ON todo_comments(todo_id);

-- Todo contacts
CREATE TABLE todo_contacts (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_contacts_todo_id ON todo_contacts(todo_id);

-- Todo resources
CREATE TABLE todo_resources (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    text    TEXT    NOT NULL
);

CREATE INDEX idx_todo_resources_todo_id ON todo_resources(todo_id);

-- Todo relations
CREATE TABLE todo_relations (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    rel_type TEXT    NOT NULL DEFAULT 'PARENT',
    rel_uid  TEXT
);

CREATE INDEX idx_todo_relations_todo_id ON todo_relations(todo_id);

-- Todo alarm state
CREATE TABLE todo_alarm_state (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    alarm_id   INTEGER NOT NULL REFERENCES todo_alarms(id) ON DELETE CASCADE,
    todo_id    INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    trigger_at TEXT    NOT NULL,
    fired_at   TEXT,
    acked_at   TEXT,
    snoozed_to TEXT
);

CREATE UNIQUE INDEX idx_todo_alarm_state_unique  ON todo_alarm_state(alarm_id, trigger_at);
CREATE INDEX        idx_todo_alarm_state_todo_id ON todo_alarm_state(todo_id);

-- Todo recurrence cache
CREATE TABLE todo_recurrence_instances (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id     INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    original_id INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    instance_at TEXT    NOT NULL,
    is_override INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE UNIQUE INDEX idx_todo_recurrence_unique      ON todo_recurrence_instances(todo_id, instance_at);
CREATE INDEX        idx_todo_recurrence_todo         ON todo_recurrence_instances(todo_id);
CREATE INDEX        idx_todo_recurrence_instance_at  ON todo_recurrence_instances(instance_at);
CREATE INDEX        idx_todo_recurrence_original     ON todo_recurrence_instances(original_id);

-- +goose Down
DROP TABLE IF EXISTS todo_recurrence_instances;
DROP TABLE IF EXISTS todo_alarm_state;
DROP TABLE IF EXISTS todo_relations;
DROP TABLE IF EXISTS todo_resources;
DROP TABLE IF EXISTS todo_contacts;
DROP TABLE IF EXISTS todo_comments;
DROP TABLE IF EXISTS todo_attachments;
DROP TABLE IF EXISTS todo_attendees;
DROP TABLE IF EXISTS todo_alarm_attendees;
DROP TABLE IF EXISTS todo_alarms;
DROP TABLE IF EXISTS todo_categories;
DROP TABLE IF EXISTS todos;
DROP TABLE IF EXISTS recurrence_instances;
DROP TABLE IF EXISTS alarm_state;
DROP TABLE IF EXISTS event_relations;
DROP TABLE IF EXISTS event_resources;
DROP TABLE IF EXISTS event_contacts;
DROP TABLE IF EXISTS event_comments;
DROP TABLE IF EXISTS event_attachments;
DROP TABLE IF EXISTS event_attendees;
DROP TABLE IF EXISTS event_alarm_attendees;
DROP TABLE IF EXISTS event_alarms;
DROP TABLE IF EXISTS event_categories;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS calendars;
