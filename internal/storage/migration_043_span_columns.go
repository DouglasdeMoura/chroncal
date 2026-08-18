package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/douglasdemoura/chroncal/internal/duration"
)

// migration043Version matches the file name a SQL migration would carry.
// The heal needs the Go span parser, so it is a Go migration instead of a
// file in db/migrations.
const migration043Version = 43

// newSpanColumnMigration heals the span columns that the parsers wrote
// before they validated a span with duration.ValidateSpan (issue #581).
// It clears an events.duration or a todos.duration that fails the span
// rule.
//
// The clear is safe. end_time and due stay authoritative, and an
// invalid span never had a usable meaning. Without the repair, a legacy
// negative or malformed value blocks every later edit at the service
// boundary, and it can reach the server on a push.
//
// The migration runs once per database, so no startup pays for the two
// table scans. An older binary can still write a bad span after the
// migration runs. That window is small: the service boundary now
// validates every write path, so only a downgrade re-opens it.
func newSpanColumnMigration() *goose.Migration {
	return goose.NewGoMigration(
		migration043Version,
		&goose.GoFunc{RunTx: healSpanColumnsTx},
		// The Down direction restores nothing. The migration deletes no
		// row and no column, and the cleared values were unusable.
		&goose.GoFunc{RunTx: func(context.Context, *sql.Tx) error { return nil }},
	)
}

func healSpanColumnsTx(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"events", "todos"} {
		var ids []int64
		err := func() error {
			rows, err := tx.QueryContext(ctx,
				`SELECT id, duration FROM `+table+` WHERE COALESCE(duration, '') != ''`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var span string
				if err := rows.Scan(&id, &span); err != nil {
					return err
				}
				if duration.ValidateSpan(span) != nil {
					ids = append(ids, id)
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return fmt.Errorf("scan %s: %w", table, err)
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+table+` SET duration = NULL WHERE id = ?`, id); err != nil {
				return fmt.Errorf("heal %s row %d: %w", table, id, err)
			}
		}
	}
	return nil
}
