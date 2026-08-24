package storage

import (
	"database/sql"
	"fmt"
)

// Open connects to the database at dbPath and makes it ready for use.
// Open only composes the bootstrap phases, in order:
//
//  1. openDB opens the pool and applies the connection settings.
//  2. runMigrations applies every SQL migration and Go migration.
//  3. ensureCredentialNamespace scopes the keyring namespace and records
//     the credential location.
//  4. heal runs every startup heal step, in the declared order.
//
// A failure in any phase closes the connection and fails Open.
func Open(dbPath string) (*sql.DB, *Queries, error) {
	conn, err := openDB(dbPath)
	if err != nil {
		return nil, nil, err
	}

	if err := runMigrations(conn); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("run migrations: %w", err)
	}

	if err := ensureCredentialNamespace(conn, dbPath); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("initialize credential namespace: %w", err)
	}

	q := New(conn)
	if err := heal(conn, q); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, q, nil
}
