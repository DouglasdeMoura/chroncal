package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/douglasdemoura/chroncal/db"
)

func Open(dbPath string) (*sql.DB, *Queries, error) {
	// Encode pragmas in the DSN so every pooled connection gets them,
	// not just the first one. PRAGMA foreign_keys is per-connection in
	// SQLite; setting it via Exec on the pool only affects one conn.
	// _txlock=immediate makes every read-write transaction acquire SQLite's
	// write lock at BEGIN instead of lazily on first write. This serializes
	// read-modify-write flows (e.g. appending an EXDATE to a master) so a
	// concurrent writer cannot slip in between the read and the write and get
	// its change silently clobbered, and it avoids the deferred-transaction
	// upgrade deadlock that returns SQLITE_BUSY immediately. SQLite already
	// allows only one writer at a time, so this costs no real concurrency.
	dsn := dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}

	// A plain ":memory:" database is private to each connection with
	// modernc.org/sqlite, so migrations applied on one pooled connection are
	// invisible to the next. Pin the pool to a single connection so every
	// query — including concurrent ones — sees the same schema and data.
	// File-backed databases keep the default unbounded pool.
	if dbPath == ":memory:" {
		conn.SetMaxOpenConns(1)
	}

	if err := runMigrations(conn); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("run migrations: %w", err)
	}

	q := New(conn)
	if err := backfillAlarmUIDs(conn, q); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("backfill alarm uids: %w", err)
	}
	if err := purgeLibicalDiagnosticXProps(conn); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("purge libical diagnostic x-props: %w", err)
	}

	return conn, q, nil
}

// purgeLibicalDiagnosticXProps drops X-LIC-ERROR / X-LIC-ERRORTYPE rows that
// older imports stored as round-trip x_properties. libical emits those as
// inline parse-error markers; serializing them back out gets the resource
// rejected with HTTP 400 by strict CalDAV servers (Google in particular).
// Import and export both filter them now, but rows already in the DB still
// poison every push until they're gone — so we sweep them on startup.
func purgeLibicalDiagnosticXProps(conn *sql.DB) error {
	_, err := conn.ExecContext(context.Background(),
		`DELETE FROM x_properties WHERE name LIKE 'X-LIC-%'`)
	return err
}

// backfillAlarmUIDs assigns random UUIDs to alarms that have empty UIDs.
// This runs once after upgrade from pre-UID schema.
func backfillAlarmUIDs(conn *sql.DB, q *Queries) error {
	ctx := context.Background()

	alarms, err := q.ListAlarmsWithEmptyUID(ctx)
	if err != nil {
		return fmt.Errorf("list alarms with empty uid: %w", err)
	}
	todoAlarms, err := q.ListTodoAlarmsWithEmptyUID(ctx)
	if err != nil {
		return fmt.Errorf("list todo alarms with empty uid: %w", err)
	}
	if len(alarms) == 0 && len(todoAlarms) == 0 {
		return nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	qtx := q.WithTx(tx)
	for _, a := range alarms {
		if err := qtx.UpdateAlarmUID(ctx, UpdateAlarmUIDParams{
			Uid: StringToNullable(uuid.New().String()),
			ID:  a.ID,
		}); err != nil {
			return err
		}
	}
	for _, a := range todoAlarms {
		if err := qtx.UpdateTodoAlarmUID(ctx, UpdateTodoAlarmUIDParams{
			Uid: StringToNullable(uuid.New().String()),
			ID:  a.ID,
		}); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit alarm UID backfill: %w", err)
	}
	return nil
}

func runMigrations(conn *sql.DB) error {
	migrationsFS, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		return fmt.Errorf("sub migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationsFS)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	_, err = provider.Up(context.Background())
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
