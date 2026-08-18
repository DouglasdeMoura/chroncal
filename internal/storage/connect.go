package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/douglasdemoura/chroncal/db"
	"github.com/douglasdemoura/chroncal/internal/fileid"
	"github.com/douglasdemoura/chroncal/internal/model"
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

	if err := ensureCredentialNamespace(conn, dbPath); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("initialize credential namespace: %w", err)
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
	if err := normalizeAlarmRepeatPairs(conn); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("normalize alarm repeat pairs: %w", err)
	}

	return conn, q, nil
}

// CredentialScopes identifies the current external credential namespace and
// any prior locations from which credentials may be copied after a database
// move. Combine the in-database UUID with a canonical path. A copied
// database then cannot share mutable keyring/file entries with its source.
type PreviousCredentialScope struct {
	Namespace    string
	MaxAccountID int64
}

type CredentialScopes struct {
	Current  string
	Previous []PreviousCredentialScope
}

func GetCredentialScopes(ctx context.Context, conn *sql.DB, dbPath string) (CredentialScopes, error) {
	var databaseUUID string
	if err := conn.QueryRowContext(ctx,
		`SELECT namespace FROM credential_namespace WHERE id = 1`,
	).Scan(&databaseUUID); err != nil {
		return CredentialScopes{}, fmt.Errorf("read credential namespace: %w", err)
	}
	if _, err := uuid.Parse(databaseUUID); err != nil {
		return CredentialScopes{}, fmt.Errorf("invalid credential namespace %q: %w", databaseUUID, err)
	}

	currentLocation, err := credentialLocation(dbPath)
	if err != nil {
		return CredentialScopes{}, err
	}
	current := credentialScope(databaseUUID, currentLocation)
	if currentLocation == "" {
		return CredentialScopes{Current: current}, nil
	}

	rows, err := conn.QueryContext(ctx, `SELECT location, max_account_id FROM credential_locations ORDER BY location`)
	if err != nil {
		return CredentialScopes{}, fmt.Errorf("list credential locations: %w", err)
	}
	defer rows.Close()
	var previous []PreviousCredentialScope
	for rows.Next() {
		var location string
		var maxAccountID int64
		if err := rows.Scan(&location, &maxAccountID); err != nil {
			return CredentialScopes{}, fmt.Errorf("scan credential location: %w", err)
		}
		if location != currentLocation {
			previous = append(previous, PreviousCredentialScope{
				Namespace:    credentialScope(databaseUUID, location),
				MaxAccountID: maxAccountID,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return CredentialScopes{}, fmt.Errorf("list credential locations: %w", err)
	}
	return CredentialScopes{Current: current, Previous: previous}, nil
}

func ensureCredentialNamespace(conn *sql.DB, dbPath string) error {
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO credential_namespace (id, namespace) VALUES (1, ?)`,
		uuid.NewString(),
	); err != nil {
		return err
	}
	location, err := credentialLocation(dbPath)
	if err != nil {
		return err
	}
	if location != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO credential_locations (location, max_account_id)
			VALUES (?, (SELECT COALESCE(MAX(id), 0) FROM accounts))
			ON CONFLICT(location) DO UPDATE SET
			    max_account_id = excluded.max_account_id`,
			location,
		); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE credential_namespace SET current_location = ? WHERE id = 1`,
		location,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = GetCredentialScopes(ctx, conn, dbPath)
	return err
}

func credentialLocation(dbPath string) (string, error) {
	if dbPath == ":memory:" {
		return "", nil
	}
	identity, err := fileid.Identity(dbPath)
	if err != nil {
		return "", fmt.Errorf("identify database file: %w", err)
	}
	return identity, nil
}
func credentialScope(databaseUUID, location string) string {
	if location == "" {
		return databaseUUID
	}
	pathHash := sha256.Sum256([]byte(location))
	return fmt.Sprintf("%s-%x", databaseUUID, pathHash)
}

// purgeLibicalDiagnosticXProps drops X-LIC-ERROR / X-LIC-ERRORTYPE rows that
// older imports stored as round-trip x_properties. libical emits those as
// inline parse-error markers. A serialize of them back out gets the resource
// rejected with HTTP 400 by strict CalDAV servers (Google in particular).
// Import and export both filter them now. Rows already in the DB still
// poison every push until they are gone. Sweep them on startup.
func purgeLibicalDiagnosticXProps(conn *sql.DB) error {
	_, err := conn.ExecContext(context.Background(),
		`DELETE FROM x_properties WHERE name LIKE 'X-LIC-%'`)
	return err
}

// normalizeAlarmRepeatPairs heals alarm rows written before the parsers
// validated REPEAT and DURATION (issues #580 and #581). It clamps the
// repeat count, clears an interval that fails model.ValidAlarmDuration,
// and clears an unpaired value. Without this repair, export and the
// next pull disagree with the stored row. The mismatch deletes and
// recreates the alarm row, and the cascade discards the alarm state.
//
// The pass does not delete a row whose trigger_value fails
// model.ParseableAlarmTrigger. Such a row is inert: the fire path
// cannot read it and export omits its VALARM. A delete would cascade
// away the alarm state. It could not reach the server, because nothing
// marks the resource dirty. It would also destroy an RFC-valid trigger
// from another client that only this build cannot represent.
//
// The writes for each table run in one transaction, like
// backfillAlarmUIDs, and a failure is fatal to Open, like every startup
// pass here. The function runs on every startup. It changes nothing
// when all rows are healthy.
func normalizeAlarmRepeatPairs(conn *sql.DB) error {
	ctx := context.Background()
	for _, table := range []string{"event_alarms", "todo_alarms"} {
		type fix struct {
			id       int64
			repeat   int
			duration string
		}
		var fixes []fix
		err := func() error {
			rows, err := conn.QueryContext(ctx,
				`SELECT id, repeat, COALESCE(duration, '') FROM `+table+
					` WHERE repeat > 0 OR COALESCE(duration, '') != ''`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var a model.Alarm
				if err := rows.Scan(&id, &a.Repeat, &a.Duration); err != nil {
					return err
				}
				healed := a
				healed.Repeat = min(healed.Repeat, model.MaxAlarmRepeat)
				if healed.Duration != "" && !model.ValidAlarmDuration(healed.Duration) {
					healed.Duration = ""
				}
				if !healed.RepeatPaired() {
					healed.Repeat, healed.Duration = 0, ""
				}
				if healed.Repeat != a.Repeat || healed.Duration != a.Duration {
					fixes = append(fixes, fix{id, healed.Repeat, healed.Duration})
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return fmt.Errorf("scan %s: %w", table, err)
		}
		if len(fixes) == 0 {
			continue
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("heal %s: %w", table, err)
		}
		for _, f := range fixes {
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+table+` SET repeat = ?, duration = NULLIF(?, '') WHERE id = ?`,
				f.repeat, f.duration, f.id); err != nil {
				tx.Rollback()
				return fmt.Errorf("heal %s row %d: %w", table, f.id, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("heal %s: %w", table, err)
		}
	}
	return nil
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
