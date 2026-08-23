package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/fileid"
)

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

// ensureCredentialNamespace is the credential-namespace phase of Open. It
// seeds the database UUID on first run and records the current location of
// the database file, so credentials stay scoped after a database move.
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
