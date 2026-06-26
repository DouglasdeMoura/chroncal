-- +goose Up

-- Store an inline (ENCODING=BASE64;VALUE=BINARY) ATTACH payload for AUDIO
-- alarms so an embedded sound survives import/export round-trips (issue #298).
ALTER TABLE event_alarms ADD COLUMN attach_binary BLOB;
ALTER TABLE todo_alarms ADD COLUMN attach_binary BLOB;

-- +goose Down
ALTER TABLE todo_alarms DROP COLUMN attach_binary;
ALTER TABLE event_alarms DROP COLUMN attach_binary;
