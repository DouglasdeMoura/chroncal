-- +goose Up

-- Per-row revision counter for optimistic concurrency on push. Every
-- mutation that flips dirty=1 (a local edit) bumps rev. Push captures rev
-- before exporting the body, then clears dirty only if rev is unchanged.
-- A local edit that lands during the multi-second PUT round-trip bumps rev,
-- so the post-PUT clear no-ops and the edit survives to the next push
-- instead of being silently dropped (lost update).
ALTER TABLE sync_resources ADD COLUMN rev INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sync_resources DROP COLUMN rev;
