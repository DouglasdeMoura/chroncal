-- +goose Up

-- FTS5 virtual tables for full-text search.
-- Standalone tables (no content= option) — FTS owns its copy of text.
-- App-level sync keeps these in sync with the source tables.

CREATE VIRTUAL TABLE events_fts USING fts5(
    title, description, location, categories,
    tokenize='unicode61 remove_diacritics 0'
);

CREATE VIRTUAL TABLE todos_fts USING fts5(
    summary, description, location, categories,
    tokenize='unicode61 remove_diacritics 0'
);

-- Backfill existing events into FTS.
INSERT INTO events_fts (rowid, title, description, location, categories)
SELECT e.id, e.title, COALESCE(e.description, ''), COALESCE(e.location, ''),
       COALESCE((SELECT GROUP_CONCAT(ec.category, ' ') FROM event_categories ec WHERE ec.event_id = e.id), '')
FROM events e;

-- Backfill existing todos into FTS.
INSERT INTO todos_fts (rowid, summary, description, location, categories)
SELECT t.id, t.summary, COALESCE(t.description, ''), COALESCE(t.location, ''),
       COALESCE((SELECT GROUP_CONCAT(tc.category, ' ') FROM todo_categories tc WHERE tc.todo_id = t.id), '')
FROM todos t;

-- +goose Down
DROP TABLE IF EXISTS todos_fts;
DROP TABLE IF EXISTS events_fts;
