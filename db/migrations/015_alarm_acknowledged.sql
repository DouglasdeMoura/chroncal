-- +goose Up
-- Add ACKNOWLEDGED property (RFC 9074) for iCal round-trip fidelity.
-- This column is never used for local alarm state decisions (alarm_state.acked_at
-- remains authoritative). It is only preserved on import and re-emitted on export.
ALTER TABLE event_alarms ADD COLUMN acknowledged TEXT NOT NULL DEFAULT '';
ALTER TABLE todo_alarms ADD COLUMN acknowledged TEXT NOT NULL DEFAULT '';

-- +goose Down
-- SQLite does not support DROP COLUMN before 3.35.0; recreate tables if needed.
