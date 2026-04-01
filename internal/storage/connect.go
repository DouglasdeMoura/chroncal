package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io/fs"
	"strings"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	sqlite "modernc.org/sqlite"

	"github.com/douglasdemoura/chroncal/db"
)

func init() {
	sqlite.MustRegisterFunction("lower_unicode", &sqlite.FunctionImpl{
		NArgs:         1,
		Deterministic: true,
		Scalar: func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if s, ok := args[0].(string); ok {
				return strings.ToLower(s), nil
			}
			return args[0], nil
		},
	})
}

func Open(dbPath string) (*sql.DB, *Queries, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := conn.Exec(p); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("exec pragma %q: %w", p, err)
		}
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
	if err := syncFTSIndex(conn, q); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("sync fts index: %w", err)
	}

	return conn, q, nil
}

// syncFTSIndex checks if the FTS indexes are in sync with the source tables.
// If row counts differ, it rebuilds the index. This handles writes that
// bypass the service layer.
func syncFTSIndex(_ *sql.DB, q *Queries) error {
	ctx := context.Background()

	var eventCount, ftsEventCount int64
	if err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&eventCount); err != nil {
		return fmt.Errorf("count events: %w", err)
	}
	if err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events_fts").Scan(&ftsEventCount); err != nil {
		// Table may not exist yet (pre-migration); skip silently.
		return nil
	}
	if eventCount != ftsEventCount {
		if err := q.RebuildEventsFTS(ctx); err != nil {
			return fmt.Errorf("rebuild events fts: %w", err)
		}
	}

	var todoCount, ftsTodoCount int64
	if err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM todos").Scan(&todoCount); err != nil {
		return fmt.Errorf("count todos: %w", err)
	}
	if err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM todos_fts").Scan(&ftsTodoCount); err != nil {
		return nil
	}
	if todoCount != ftsTodoCount {
		if err := q.RebuildTodosFTS(ctx); err != nil {
			return fmt.Errorf("rebuild todos fts: %w", err)
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
	if len(alarms) == 0 {
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

	todoAlarms, err := q.ListTodoAlarmsWithEmptyUID(ctx)
	if err != nil {
		return fmt.Errorf("list todo alarms with empty uid: %w", err)
	}
	if len(todoAlarms) == 0 {
		return tx.Commit()
	}
	for _, a := range todoAlarms {
		if err := qtx.UpdateTodoAlarmUID(ctx, UpdateTodoAlarmUIDParams{
			Uid: StringToNullable(uuid.New().String()),
			ID:  a.ID,
		}); err != nil {
			return err
		}
	}

	return tx.Commit()
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
