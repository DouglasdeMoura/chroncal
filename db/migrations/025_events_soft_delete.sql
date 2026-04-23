-- +goose Up
ALTER TABLE events ADD COLUMN deleted_at TEXT;
CREATE INDEX idx_events_deleted_at ON events(deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_events_deleted_at;
ALTER TABLE events DROP COLUMN deleted_at;
