-- +goose Up
-- Credentials live outside SQLite (OS keyring or files), so account IDs alone
-- are not globally unique when more than one chroncal database is used. Keep a
-- stable random database UUID plus every filesystem location at which this
-- database has been opened. The external credential scope combines both: a
-- copied database gets a new location scope, while a moved database can still
-- read credentials from its prior scope.
CREATE TABLE credential_namespace (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    namespace TEXT NOT NULL UNIQUE,
    current_location TEXT NOT NULL DEFAULT ''
);

CREATE TABLE credential_locations (
    location TEXT PRIMARY KEY,
    max_account_id INTEGER NOT NULL CHECK (max_account_id >= 0)
);

-- +goose Down
DROP TABLE credential_locations;
DROP TABLE credential_namespace;
