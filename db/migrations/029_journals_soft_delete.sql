-- +goose Up
ALTER TABLE journals ADD COLUMN deleted_at TEXT;
CREATE INDEX idx_journals_deleted_at ON journals(deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_journals_deleted_at;
ALTER TABLE journals DROP COLUMN deleted_at;
