-- +goose Up
ALTER TABLE event_alarms ADD COLUMN attach_uri TEXT NOT NULL DEFAULT '';
ALTER TABLE event_alarms ADD COLUMN attach_fmttype TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_alarms ADD COLUMN attach_uri TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_alarms ADD COLUMN attach_fmttype TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE event_alarms DROP COLUMN attach_fmttype;
ALTER TABLE event_alarms DROP COLUMN attach_uri;
ALTER TABLE todo_alarms DROP COLUMN attach_fmttype;
ALTER TABLE todo_alarms DROP COLUMN attach_uri;
