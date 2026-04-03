-- +goose Up

-- Generic key-value store for unknown/extension properties on any component.
-- owner_type: 'event', 'todo', 'journal'
-- owner_id: FK to the owning row's id
CREATE TABLE x_properties (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type TEXT NOT NULL,  -- 'event' | 'todo' | 'journal'
    owner_id INTEGER NOT NULL,
    name TEXT NOT NULL,        -- e.g. 'X-GOOGLE-CONFERENCE' or 'X-LIC-ERROR'
    value TEXT NOT NULL DEFAULT '',
    params TEXT NOT NULL DEFAULT '{}'  -- JSON object: {"KEY": ["val1"], "OTHER": ["val"]}
);

CREATE INDEX idx_xprops_owner ON x_properties(owner_type, owner_id);

-- +goose Down
DROP TABLE IF EXISTS x_properties;
