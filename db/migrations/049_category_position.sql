-- +goose Up
-- Categories keep the order the user wrote. A position column lets reads
-- return that order instead of an alphabetical sort. Backfill numbers the
-- existing rows by rowid, which matches the original insert order.
ALTER TABLE event_categories ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
ALTER TABLE todo_categories ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
ALTER TABLE journal_categories ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

UPDATE event_categories SET position = (
    SELECT COUNT(*) FROM event_categories AS e2
    WHERE e2.event_id = event_categories.event_id AND e2.rowid <= event_categories.rowid);
UPDATE todo_categories SET position = (
    SELECT COUNT(*) FROM todo_categories AS t2
    WHERE t2.todo_id = todo_categories.todo_id AND t2.rowid <= todo_categories.rowid);
UPDATE journal_categories SET position = (
    SELECT COUNT(*) FROM journal_categories AS j2
    WHERE j2.journal_id = journal_categories.journal_id AND j2.rowid <= journal_categories.rowid);

-- +goose Down
ALTER TABLE event_categories DROP COLUMN position;
ALTER TABLE todo_categories DROP COLUMN position;
ALTER TABLE journal_categories DROP COLUMN position;
