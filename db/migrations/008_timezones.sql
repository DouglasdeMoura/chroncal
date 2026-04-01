-- +goose Up

-- VTIMEZONE storage for iCal round-trip fidelity.
-- Preserves raw VTIMEZONE component data from imports so non-standard
-- or custom timezone definitions can be faithfully re-exported.
CREATE TABLE timezones (
    tzid           TEXT PRIMARY KEY,
    vtimezone_data TEXT NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- +goose Down
DROP TABLE IF EXISTS timezones;
