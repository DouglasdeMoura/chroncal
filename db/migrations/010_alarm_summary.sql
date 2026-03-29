-- +goose Up
ALTER TABLE event_alarms ADD COLUMN summary TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_alarms ADD COLUMN summary TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE event_alarms DROP COLUMN summary;
ALTER TABLE todo_alarms DROP COLUMN summary;
