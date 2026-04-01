-- +goose Up

-- FTS5 virtual tables for full-text search.
-- Standalone tables (no content= option) — FTS owns its copy of text.
-- App-level sync keeps these in sync with the source tables.

CREATE VIRTUAL TABLE events_fts USING fts5(
    title, description, location, categories,
    tokenize='unicode61 remove_diacritics 2'
);

CREATE VIRTUAL TABLE todos_fts USING fts5(
    summary, description, location, categories,
    tokenize='unicode61 remove_diacritics 2'
);

-- No backfill here — migration 011 drops and rebuilds these tables
-- with trigger-maintained equivalents and its own backfill.

-- +goose Down
DROP TABLE IF EXISTS todos_fts;
DROP TABLE IF EXISTS events_fts;
