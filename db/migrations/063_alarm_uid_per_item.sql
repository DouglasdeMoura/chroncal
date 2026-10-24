-- +goose Up
-- RFC 9074 needs an alarm UID that is unique in its component only.
-- Servers and clients put one VALARM UID on two resources, for example on
-- a copy of an event in another calendar. The indexes of migration 044
-- refused such a UID, so the store kept a minted UID for the row and the
-- alarm state of the row could go (issue #922). The new indexes refuse a
-- second use of a UID in one event or in one todo only.
DROP INDEX IF EXISTS idx_event_alarms_uid;
CREATE UNIQUE INDEX idx_event_alarms_event_uid ON event_alarms(event_id, uid) WHERE uid IS NOT NULL;
DROP INDEX IF EXISTS idx_todo_alarms_uid;
CREATE UNIQUE INDEX idx_todo_alarms_todo_uid ON todo_alarms(todo_id, uid) WHERE uid IS NOT NULL;

-- uid_unverified marks a UID that a build before this migration wrote.
-- Such a build minted a UID when another item held the UID of the file, so
-- the UID can be a minted UID. A write that confirms the UID clears the
-- mark. model.PairAlarms repairs a minted UID on a marked row only.
ALTER TABLE event_alarms ADD COLUMN uid_unverified INTEGER NOT NULL DEFAULT 0;
UPDATE event_alarms SET uid_unverified = 1 WHERE uid IS NOT NULL;
ALTER TABLE todo_alarms ADD COLUMN uid_unverified INTEGER NOT NULL DEFAULT 0;
UPDATE todo_alarms SET uid_unverified = 1 WHERE uid IS NOT NULL;

-- +goose Down
ALTER TABLE event_alarms DROP COLUMN uid_unverified;
ALTER TABLE todo_alarms DROP COLUMN uid_unverified;
-- The old indexes refuse a UID on two items. The row with the lowest ID
-- keeps the UID, and the other rows lose it. The startup heal gives a new
-- UID to a fireable alarm with no UID. A preserved foreign alarm keeps no
-- UID, because the store does not mint a UID for it (issue #586).
DROP INDEX IF EXISTS idx_event_alarms_event_uid;
UPDATE event_alarms SET uid = NULL
WHERE uid IS NOT NULL
  AND id NOT IN (SELECT MIN(id) FROM event_alarms WHERE uid IS NOT NULL GROUP BY uid);
CREATE UNIQUE INDEX idx_event_alarms_uid ON event_alarms(uid) WHERE uid IS NOT NULL;
DROP INDEX IF EXISTS idx_todo_alarms_todo_uid;
UPDATE todo_alarms SET uid = NULL
WHERE uid IS NOT NULL
  AND id NOT IN (SELECT MIN(id) FROM todo_alarms WHERE uid IS NOT NULL GROUP BY uid);
CREATE UNIQUE INDEX idx_todo_alarms_uid ON todo_alarms(uid) WHERE uid IS NOT NULL;
