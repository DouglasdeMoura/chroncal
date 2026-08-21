-- +goose Up

-- Consecutive failed push attempts per resource. The engine and the sync
-- doctor increment the counter and store the error after every failed
-- export, parse, or PUT. A successful push resets both. A resource that
-- fails the same way on every sync then shows a growing count: the wedge
-- that 'chroncal sync doctor' reports.
ALTER TABLE sync_resources ADD COLUMN push_fail_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_resources ADD COLUMN last_push_error TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sync_resources DROP COLUMN last_push_error;
ALTER TABLE sync_resources DROP COLUMN push_fail_count;
