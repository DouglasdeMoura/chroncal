-- +goose Up

-- Calendar containers — the top-level organizer for events and todos.
CREATE TABLE calendars (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    color       TEXT    NOT NULL DEFAULT '#7C3AED',
    description TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

INSERT INTO calendars (name) VALUES ('Personal');

-- +goose Down
DROP TABLE IF EXISTS calendars;
