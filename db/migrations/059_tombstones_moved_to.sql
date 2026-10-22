-- +goose Up
-- A local move between two calendars of one account leaves a tombstone in
-- the source calendar. moved_to_calendar_id names the target calendar of
-- that move. A NULL marks a plain delete. The tombstone pass reads it to
-- tell a move apart from a delete of a UID that lives in two calendars.
-- See issue #841.
ALTER TABLE tombstones ADD COLUMN moved_to_calendar_id INTEGER
    REFERENCES calendars(id) ON DELETE SET NULL;

-- A tombstone from before the upgrade keeps a NULL. The old rows carry no
-- record of a move. A plain delete beside an unrelated import of the same
-- UID looks the same as a move. legacy_move marks those rows: the tombstone
-- pass applies the old safety wait to them only (the view
-- move_wait_tombstones). A new tombstone gets 0.
ALTER TABLE tombstones ADD COLUMN legacy_move INTEGER NOT NULL DEFAULT 0;
UPDATE tombstones SET legacy_move = 1;

-- version counts the rewrites of a tombstone. A local move changes the
-- move target of a tombstone without a sync lock. The tombstone pass reads
-- the version before its DELETE and again before it acts on the answer. A
-- new version means a rewrite in that window, so the pass skips the
-- tombstone for this pass.
ALTER TABLE tombstones ADD COLUMN version INTEGER NOT NULL DEFAULT 0;

-- source_gone marks a tombstone whose server object is gone, while an open
-- move conflict of a third calendar holds the tombstone. The sync row of the
-- source keeps its href and its etag. The local pick of the conflict that
-- holds the tombstone reads the flag and finishes the move: the source object is
-- already deleted. A new write of the tombstone resets the flag.
ALTER TABLE tombstones ADD COLUMN source_gone INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE tombstones DROP COLUMN source_gone;
ALTER TABLE tombstones DROP COLUMN version;
ALTER TABLE tombstones DROP COLUMN legacy_move;
ALTER TABLE tombstones DROP COLUMN moved_to_calendar_id;
