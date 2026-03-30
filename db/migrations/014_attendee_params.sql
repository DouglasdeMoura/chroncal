-- +goose Up
ALTER TABLE event_attendees ADD COLUMN cutype TEXT NOT NULL DEFAULT '';
ALTER TABLE event_attendees ADD COLUMN rsvp TEXT NOT NULL DEFAULT '';
ALTER TABLE event_attendees ADD COLUMN sent_by TEXT NOT NULL DEFAULT '';
ALTER TABLE event_attendees ADD COLUMN delegated_to TEXT NOT NULL DEFAULT '';
ALTER TABLE event_attendees ADD COLUMN delegated_from TEXT NOT NULL DEFAULT '';
ALTER TABLE event_attendees ADD COLUMN member TEXT NOT NULL DEFAULT '';
ALTER TABLE event_attendees ADD COLUMN dir TEXT NOT NULL DEFAULT '';
ALTER TABLE event_attendees ADD COLUMN language TEXT NOT NULL DEFAULT '';

ALTER TABLE todo_attendees ADD COLUMN cutype TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_attendees ADD COLUMN rsvp TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_attendees ADD COLUMN sent_by TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_attendees ADD COLUMN delegated_to TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_attendees ADD COLUMN delegated_from TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_attendees ADD COLUMN member TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_attendees ADD COLUMN dir TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_attendees ADD COLUMN language TEXT NOT NULL DEFAULT '';

-- +goose Down
-- SQLite does not support DROP COLUMN before 3.35.0; recreate tables if needed.
