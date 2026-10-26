-- +goose Up
-- uid_unverified marked an alarm UID that a build before migration 063
-- wrote, for the repair of a minted UID (issue #922). Issue #924 removes the
-- repair, so the column goes. A UID that a later file names differently is
-- now a new alarm, as for every other UID.
ALTER TABLE event_alarms DROP COLUMN uid_unverified;
ALTER TABLE todo_alarms DROP COLUMN uid_unverified;

-- +goose Down
-- The column comes back with no marks: a build of migration 063 then sees
-- no UID to repair.
ALTER TABLE event_alarms ADD COLUMN uid_unverified INTEGER NOT NULL DEFAULT 0;
ALTER TABLE todo_alarms ADD COLUMN uid_unverified INTEGER NOT NULL DEFAULT 0;
