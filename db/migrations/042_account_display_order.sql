-- +goose Up
ALTER TABLE accounts ADD COLUMN display_order INTEGER NOT NULL DEFAULT 0;
UPDATE accounts SET display_order = id;

-- +goose Down
ALTER TABLE accounts DROP COLUMN display_order;
