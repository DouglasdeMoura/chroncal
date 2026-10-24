package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"

	"github.com/douglasdemoura/chroncal/db"
)

// NewMigrationProvider builds the goose provider over the embedded SQL
// migrations plus every Go migration. Production and the tests share it,
// so a provider cannot miss a Go migration and then report an applied
// version it does not know.
func NewMigrationProvider(conn *sql.DB) (*goose.Provider, error) {
	migrationsFS, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sub migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationsFS,
		goose.WithGoMigrations(newSpanColumnMigration()))
	if err != nil {
		return nil, fmt.Errorf("create goose provider: %w", err)
	}
	return provider, nil
}

// runMigrations is the migrate phase of Open. It applies every registered
// migration to the database. The registration guard from issue #691 lives
// in NewMigrationProvider.
func runMigrations(conn *sql.DB) error {
	provider, err := NewMigrationProvider(conn)
	if err != nil {
		return err
	}
	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
