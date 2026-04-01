-- +goose Up

-- Core VEVENT storage with full RFC 5545 property support.
CREATE TABLE events (
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
    UNIQUE(uid, recurrence_id)
);

-- Composite index covers calendar+time range queries and calendar-only lookups.
CREATE INDEX idx_events_cal_start  ON events(calendar_id, start_time);
CREATE INDEX idx_events_start_time ON events(start_time);
-- uid-only lookups are served by the left prefix of the UNIQUE(uid, recurrence_id) constraint.

-- Normalized junction table for CATEGORIES property.
CREATE TABLE event_categories (
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    category TEXT    NOT NULL,
    PRIMARY KEY (event_id, category)
);
CREATE INDEX idx_event_categories_category ON event_categories(category);

-- +goose Down
DROP TABLE IF EXISTS event_categories;
DROP TABLE IF EXISTS events;
