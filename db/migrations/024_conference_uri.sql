-- +goose Up
ALTER TABLE events ADD COLUMN conference_uri TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE events DROP COLUMN conference_uri;
