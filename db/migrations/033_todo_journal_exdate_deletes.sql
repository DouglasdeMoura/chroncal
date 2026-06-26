-- +goose Up
-- EXDATE-delete provenance for todos and journals, mirroring
-- event_exdate_deletes (migration 026). A row is recorded whenever a
-- per-instance delete (Delete on an override) adds an EXDATE to the master.
-- Restore consults this log so it only strips EXDATEs that a delete added,
-- never EXDATEs that arrived via import — preventing RestoreByUID from
-- silently dropping legitimate imported EXDATEs (issue #86).
CREATE TABLE todo_exdate_deletes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    recurrence_id TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (uid, recurrence_id)
);
CREATE INDEX idx_todo_exdate_deletes_calendar ON todo_exdate_deletes(calendar_id);
CREATE INDEX idx_todo_exdate_deletes_deleted_at ON todo_exdate_deletes(deleted_at);

CREATE TABLE journal_exdate_deletes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    recurrence_id TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (uid, recurrence_id)
);
CREATE INDEX idx_journal_exdate_deletes_calendar ON journal_exdate_deletes(calendar_id);
CREATE INDEX idx_journal_exdate_deletes_deleted_at ON journal_exdate_deletes(deleted_at);

-- +goose Down
DROP INDEX IF EXISTS idx_journal_exdate_deletes_deleted_at;
DROP INDEX IF EXISTS idx_journal_exdate_deletes_calendar;
DROP TABLE IF EXISTS journal_exdate_deletes;
DROP INDEX IF EXISTS idx_todo_exdate_deletes_deleted_at;
DROP INDEX IF EXISTS idx_todo_exdate_deletes_calendar;
DROP TABLE IF EXISTS todo_exdate_deletes;
