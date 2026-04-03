-- +goose Up

ALTER TABLE calendars ADD COLUMN remote_color TEXT DEFAULT '';
ALTER TABLE calendars ADD COLUMN color_dirty INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE calendars DROP COLUMN color_dirty;
ALTER TABLE calendars DROP COLUMN remote_color;
