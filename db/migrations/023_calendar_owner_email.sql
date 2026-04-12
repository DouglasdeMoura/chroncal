-- +goose Up
ALTER TABLE calendars ADD COLUMN owner_email TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE calendars DROP COLUMN owner_email;
