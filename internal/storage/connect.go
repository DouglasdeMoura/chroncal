package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/douglasdemoura/tcal/db"
)

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

	return conn, New(conn), nil
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
