-- +goose Up
-- A conflict stays recoverable after an automatic resolution. A server-wins
-- sync pass imports the server body but keeps the row, so
-- `sync resolve --pick local` can still restore the stored local body.
-- resolved_at IS NULL marks an unresolved conflict. See issue #610.
ALTER TABLE sync_conflicts ADD COLUMN resolved_at TEXT;
ALTER TABLE sync_conflicts ADD COLUMN resolution TEXT
    CHECK(resolution IS NULL OR resolution IN ('server', 'local', 'server-auto'));

-- +goose Down
ALTER TABLE sync_conflicts DROP COLUMN resolution;
ALTER TABLE sync_conflicts DROP COLUMN resolved_at;
