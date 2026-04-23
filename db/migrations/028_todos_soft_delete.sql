-- +goose Up
ALTER TABLE todos ADD COLUMN deleted_at TEXT;
CREATE INDEX idx_todos_deleted_at ON todos(deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_todos_deleted_at;
ALTER TABLE todos DROP COLUMN deleted_at;
