-- +goose Up
-- An account removal keeps remote_url, the sync rows, and the tombstones of
-- its calendars, so a later add of the account links them again. The flag
-- marks such a calendar from the removal until the relink settles its
-- unsent changes (issue #876):
--   - An edit while the calendar has no account marks its sync row.
--   - A pass of the relinked calendar uses the prompt strategy while an
--     unsent change of a pushed item exists, so a server-wins pass does not
--     adopt the server body over the edit.
-- A calendar that a removal unlinked before this migration gets the flag.
ALTER TABLE calendars ADD COLUMN relink_pending INTEGER NOT NULL DEFAULT 0;
UPDATE calendars SET relink_pending = 1
WHERE (account_id IS NULL OR account_id = 0) AND COALESCE(remote_url, '') != '';

-- +goose Down
ALTER TABLE calendars DROP COLUMN relink_pending;
