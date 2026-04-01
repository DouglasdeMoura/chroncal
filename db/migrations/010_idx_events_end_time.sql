-- +goose Up
CREATE INDEX idx_events_end_time ON events(end_time);

-- +goose Down
DROP INDEX IF EXISTS idx_events_end_time;
