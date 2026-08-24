package storage

import (
	"context"
	"database/sql"
)

// purgeLibicalDiagnosticXProps drops X-LIC-ERROR / X-LIC-ERRORTYPE rows that
// older imports stored as round-trip x_properties. libical emits those as
// inline parse-error markers. A serialize of them back out gets the resource
// rejected with HTTP 400 by strict CalDAV servers (Google in particular).
// Import and export both filter them now. Rows already in the DB still
// poison every push until they are gone. Sweep them on startup.
func purgeLibicalDiagnosticXProps(ctx context.Context, conn *sql.DB) error {
	_, err := conn.ExecContext(ctx,
		`DELETE FROM x_properties WHERE name LIKE 'X-LIC-%'`)
	return err
}
