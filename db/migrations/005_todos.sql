-- +goose Up

-- Core VTODO storage with full RFC 5545 property support.
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
    percent_complete INTEGER NOT NULL DEFAULT 0
        CHECK(percent_complete BETWEEN 0 AND 100),
    status           TEXT    NOT NULL DEFAULT 'NEEDS-ACTION'
        CHECK(status IN ('NEEDS-ACTION','COMPLETED','IN-PROCESS','CANCELLED')),
    priority         INTEGER NOT NULL DEFAULT 0
        CHECK(priority BETWEEN 0 AND 9),
    class            TEXT    NOT NULL DEFAULT 'PUBLIC'
        CHECK(class IN ('PUBLIC','PRIVATE','CONFIDENTIAL')),
    url              TEXT,
    recurrence_rule  TEXT,
    timezone         TEXT,
    sequence         INTEGER NOT NULL DEFAULT 0
        CHECK(sequence >= 0),
    exdates          TEXT,
    rdates           TEXT,
    recurrence_id    TEXT    NOT NULL DEFAULT '',
    geo              TEXT,
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    dtstamp          TEXT,
    UNIQUE(uid, recurrence_id)
);

-- Composite index covers calendar+due_date queries and calendar-only lookups.
CREATE INDEX idx_todos_cal_due    ON todos(calendar_id, due_date);
CREATE INDEX idx_todos_due_date   ON todos(due_date);
CREATE INDEX idx_todos_status     ON todos(status);
CREATE INDEX idx_todos_uid        ON todos(uid);
CREATE INDEX idx_todos_recurrence ON todos(uid, recurrence_id);

-- Normalized junction table for CATEGORIES property.
CREATE TABLE todo_categories (
    todo_id  INTEGER NOT NULL REFERENCES todos(id) ON DELETE CASCADE,
    category TEXT    NOT NULL,
    PRIMARY KEY (todo_id, category)
);
CREATE INDEX idx_todo_categories_category ON todo_categories(category);

-- +goose Down
DROP TABLE IF EXISTS todo_categories;
DROP TABLE IF EXISTS todos;
