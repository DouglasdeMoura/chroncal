-- +goose Up
CREATE TABLE todos (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    uid              TEXT    NOT NULL,
    calendar_id      INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    summary          TEXT    NOT NULL,
    description      TEXT    NOT NULL DEFAULT '',
    location         TEXT    NOT NULL DEFAULT '',
    due_date         TEXT    NOT NULL DEFAULT '',
    start_date       TEXT    NOT NULL DEFAULT '',
    duration         TEXT    NOT NULL DEFAULT '',
    completed_at     TEXT    NOT NULL DEFAULT '',
    percent_complete INTEGER NOT NULL DEFAULT 0,
    status           TEXT    NOT NULL DEFAULT 'NEEDS-ACTION',
    priority         INTEGER NOT NULL DEFAULT 0,
    class            TEXT    NOT NULL DEFAULT 'PUBLIC',
    url              TEXT    NOT NULL DEFAULT '',
    categories       TEXT    NOT NULL DEFAULT '',
    recurrence_rule  TEXT    NOT NULL DEFAULT '',
    timezone         TEXT    NOT NULL DEFAULT '',
    sequence         INTEGER NOT NULL DEFAULT 0,
    exdates          TEXT    NOT NULL DEFAULT '',
    rdates           TEXT    NOT NULL DEFAULT '',
    recurrence_id    TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(uid, recurrence_id)
);

CREATE INDEX idx_todos_calendar_id ON todos(calendar_id);
CREATE INDEX idx_todos_due_date ON todos(due_date);
CREATE INDEX idx_todos_status ON todos(status);
CREATE INDEX idx_todos_uid ON todos(uid);
CREATE INDEX idx_todos_recurrence ON todos(uid, recurrence_id);

CREATE TABLE todo_alarms (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id       INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    action        TEXT    NOT NULL DEFAULT 'DISPLAY',
    trigger_value TEXT    NOT NULL,
    description   TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_todo_alarms_todo_id ON todo_alarms(todo_id);

CREATE TABLE todo_attendees (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    todo_id     INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    email       TEXT    NOT NULL,
    name        TEXT    NOT NULL DEFAULT '',
    rsvp_status TEXT    NOT NULL DEFAULT 'NEEDS-ACTION',
    role        TEXT    NOT NULL DEFAULT 'REQ-PARTICIPANT',
    organizer   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_todo_attendees_todo_id ON todo_attendees(todo_id);

-- +goose Down
DROP TABLE todo_attendees;
DROP TABLE todo_alarms;
DROP TABLE todos;
