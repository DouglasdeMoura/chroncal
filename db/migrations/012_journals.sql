-- +goose Up

-- Core VJOURNAL storage with full RFC 5545 property support.
CREATE TABLE journals (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    uid              TEXT    NOT NULL,
    calendar_id      INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    summary          TEXT    NOT NULL,
    description      TEXT,
    start_date       TEXT,
    status           TEXT    NOT NULL DEFAULT 'FINAL'
        CHECK(status IN ('DRAFT','FINAL','CANCELLED')),
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
    dtstamp          TEXT,
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(uid, recurrence_id)
);

-- Composite index covers calendar+start_date queries and calendar-only lookups.
CREATE INDEX idx_journals_cal_start  ON journals(calendar_id, start_date);
CREATE INDEX idx_journals_start_date ON journals(start_date);
CREATE INDEX idx_journals_status     ON journals(status);
-- uid-only lookups are served by the left prefix of the UNIQUE(uid, recurrence_id) constraint.

-- Normalized junction table for CATEGORIES property.
CREATE TABLE journal_categories (
    journal_id INTEGER NOT NULL REFERENCES journals(id) ON DELETE CASCADE,
    category   TEXT    NOT NULL,
    PRIMARY KEY (journal_id, category)
);
CREATE INDEX idx_journal_categories_category ON journal_categories(category);

-- +goose Down
DROP TABLE IF EXISTS journal_categories;
DROP TABLE IF EXISTS journals;
