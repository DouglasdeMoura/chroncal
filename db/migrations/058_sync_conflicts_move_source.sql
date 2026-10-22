-- +goose Up
-- A local move between two calendars of one account can meet a remote edit
-- of the source object. The source DELETE then gets 412. The push records
-- the conflict on the target calendar and names the source calendar here.
-- A NULL marks a normal edit conflict. The row goes with the source
-- calendar: without that calendar the conflict has no second half to pick.
-- See issue #841.
ALTER TABLE sync_conflicts ADD COLUMN move_source_calendar_id INTEGER
    REFERENCES calendars(id) ON DELETE CASCADE;

-- move_state is the state of the move of a move conflict. An edit
-- conflict keeps ''. The resolver and the sync engine write it at the
-- moment the state changes:
--   open          the record; no pick yet
--   local-picked  the local pick ran; the move continues
--   finished      the source delete went out, or the server deleted the
--                 source object; the move is complete
--   cancelled     the server pick ran
--   cancel-failed the server pick moved the rows back, and the import of
--                 the server body failed; the pick runs again
--   abandoned     the moved copy is gone; no pick applies
--   detached      an unlink of the source calendar ended the move; no
--                 pick applies
ALTER TABLE sync_conflicts ADD COLUMN move_state TEXT NOT NULL DEFAULT ''
    CHECK(move_state IN ('', 'open', 'local-picked', 'finished', 'cancelled', 'cancel-failed', 'abandoned', 'detached'));

-- warned_etag is the etag of the moved copy that the last diverged-copy
-- warning of the pull named. The pull warns once per new etag.
ALTER TABLE sync_conflicts ADD COLUMN warned_etag TEXT NOT NULL DEFAULT '';

-- warned_hash is the SHA-256 hash of the body that the last diverged-copy
-- warning named, for a server that gives no etag. The pull warns once per
-- new body then.
ALTER TABLE sync_conflicts ADD COLUMN warned_hash TEXT NOT NULL DEFAULT '';

-- cancel_source_rev is the rev of the source sync row after a cancel of
-- the move. A later local restore refuses a dirty source row with another
-- rev: the user edited the item after the cancel.
ALTER TABLE sync_conflicts ADD COLUMN cancel_source_rev INTEGER NOT NULL DEFAULT 0;

-- cancel_source_row_id is the id of that source sync row. A row that a
-- delete removed and an import created again has a new id, and its rev
-- can start at the same value. 0 means that no id was recorded.
ALTER TABLE sync_conflicts ADD COLUMN cancel_source_row_id INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sync_conflicts DROP COLUMN cancel_source_row_id;
ALTER TABLE sync_conflicts DROP COLUMN cancel_source_rev;
ALTER TABLE sync_conflicts DROP COLUMN warned_hash;
ALTER TABLE sync_conflicts DROP COLUMN warned_etag;
ALTER TABLE sync_conflicts DROP COLUMN move_state;
ALTER TABLE sync_conflicts DROP COLUMN move_source_calendar_id;
