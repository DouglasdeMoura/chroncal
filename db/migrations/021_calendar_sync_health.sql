-- +goose Up

ALTER TABLE calendars ADD COLUMN last_sync_attempted_at TEXT DEFAULT '';
ALTER TABLE calendars ADD COLUMN last_sync_error TEXT DEFAULT '';

-- +goose Down
ALTER TABLE calendars DROP COLUMN last_sync_error;
ALTER TABLE calendars DROP COLUMN last_sync_attempted_at;
