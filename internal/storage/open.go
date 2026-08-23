package storage

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// openDB is the open phase of Open. It opens the pool and applies every
// connection setting through the DSN, so each pooled connection receives
// them. A PRAGMA set through Exec on the pool reaches only one connection.
//
// The connection settings, one for one:
//
//   - journal_mode(WAL): readers and the writer do not block each other.
//   - foreign_keys(ON): SQLite enforces the foreign keys. The setting is
//     per-connection in SQLite, so the DSN is the only carrier that covers
//     the whole pool.
//   - busy_timeout(5000): a lock conflict waits up to 5 s before it fails,
//     instead of failing at once.
//   - synchronous(NORMAL): SQLite syncs the WAL less strictly. This is the
//     recommended pairing with WAL.
//   - _txlock=immediate: every read-write transaction acquires SQLite's
//     write lock at BEGIN instead of lazily on first write. This serializes
//     read-modify-write flows (for example, appending an EXDATE to a
//     master), so a concurrent writer cannot slip in between the read and
//     the write and get its change silently clobbered. It also avoids the
//     deferred-transaction upgrade deadlock that returns SQLITE_BUSY
//     immediately. SQLite already allows only one writer at a time, so
//     this costs no real concurrency.
func openDB(dbPath string) (*sql.DB, error) {
	dsn := dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// A plain ":memory:" database is private to each connection with
	// modernc.org/sqlite, so migrations applied on one pooled connection are
	// invisible to the next. Pin the pool to a single connection so every
	// query — including concurrent ones — sees the same schema and data.
	// File-backed databases keep the default unbounded pool.
	if dbPath == ":memory:" {
		conn.SetMaxOpenConns(1)
	}
	return conn, nil
}
