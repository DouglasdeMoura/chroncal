-- +goose Up

-- Track when each calendar was last synced for status display.
ALTER TABLE calendars ADD COLUMN last_sync_at TEXT DEFAULT '';

-- +goose Down
ALTER TABLE calendars DROP COLUMN last_sync_at;
