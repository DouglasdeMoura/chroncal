-- +goose Up
-- delete_token names the soft-delete statement that put a todo or a journal
-- row in the trash (issue #928). Each soft-delete statement writes one new
-- token, the highest token of the table plus one, on every row that it
-- hides. A restore by UID of a series in the trash then brings back the
-- rows of the series delete only. A row that an earlier delete hid, for
-- example a deleted occurrence, stays in the trash. A row from before this
-- migration has token 0. Events keep their own token (migration 057).
ALTER TABLE todos ADD COLUMN delete_token INTEGER NOT NULL DEFAULT 0;
ALTER TABLE journals ADD COLUMN delete_token INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_todos_delete_token ON todos(delete_token);
CREATE INDEX idx_journals_delete_token ON journals(delete_token);

-- +goose Down
DROP INDEX IF EXISTS idx_journals_delete_token;
DROP INDEX IF EXISTS idx_todos_delete_token;
ALTER TABLE journals DROP COLUMN delete_token;
ALTER TABLE todos DROP COLUMN delete_token;
