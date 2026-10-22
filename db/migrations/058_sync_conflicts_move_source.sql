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
-- conflict keeps ''. The resolver and the sync engine write it when the
-- state changes. These are the states:
--   open          The push recorded the conflict. No pick ran yet.
--   local-picked  The local pick ran. The move continues.
--   finished      The source delete went out, or the server deleted the
--                 source object. The move is complete.
--   cancelled     The server pick ran.
--   cancel-failed The server pick moved the rows back. Then the import of
--                 the server body failed. The pick runs again.
--   abandoned     The moved copy is gone. No pick applies.
--   detached      An unlink of the source calendar ended the move. No
--                 pick applies.
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
