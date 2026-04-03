package storage

import (
	"context"
	"database/sql"
)

// MarkResourceDirty sets dirty=1 on the sync_resources row for the given
// calendar_id + uid pair. If no sync_resources row exists (local-only item),
// this is a no-op. Called by service-layer mutations (Create, Update, Delete,
// ReplaceAlarms, etc.) when the item belongs to a synced calendar.
func MarkResourceDirty(ctx context.Context, db *sql.DB, calendarID int64, uid string) error {
	if calendarID == 0 || uid == "" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`UPDATE sync_resources SET dirty = 1 WHERE calendar_id = ? AND uid = ?`,
		calendarID, uid,
	)
	return err
}

// CreateTombstoneIfSynced inserts a tombstone row if the resource was
// previously synced (has a sync_resources row with a non-empty remote_url).
// Returns true if a tombstone was created.
func CreateTombstoneIfSynced(ctx context.Context, db *sql.DB, calendarID int64, uid string) (bool, error) {
	if calendarID == 0 || uid == "" {
		return false, nil
	}
	var remoteURL string
	err := db.QueryRowContext(ctx,
		`SELECT remote_url FROM sync_resources WHERE calendar_id = ? AND uid = ?`,
		calendarID, uid,
	).Scan(&remoteURL)
	if err != nil || remoteURL == "" {
		return false, nil
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (?, ?, ?)`,
		calendarID, uid, remoteURL,
	)
	if err != nil {
		return false, err
	}
	return true, nil
}
