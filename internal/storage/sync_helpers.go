package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MarkResourceDirty marks a resource as dirty for sync. If the calendar is
// linked to an account (synced), this upserts a sync_resources row with
// dirty=1. For local-only calendars this is a no-op.
// Called by service-layer mutations (Create, Update, ReplaceAlarms, etc.).
func MarkResourceDirty(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) error {
	if calendarID == 0 || uid == "" {
		return nil
	}
	// Only act if the calendar is linked to an account.
	var accountID *int64
	err := db.QueryRowContext(ctx,
		`SELECT account_id FROM calendars WHERE id = ?`, calendarID,
	).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if accountID == nil || *accountID == 0 {
		return nil
	}
	// Bump rev on every edit so a concurrent push (which captured the prior
	// rev before exporting the body) refuses to clear dirty and silently drop
	// this edit. See FinalizePushedResource and issue #92.
	_, err = db.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy)
		 VALUES (?, ?, ?, 1, 'sync-token')
		 ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, rev = rev + 1`,
		calendarID, uid, ownerType,
	)
	return err
}

// CreateTombstoneIfSynced inserts a tombstone row if the resource was
// previously synced (has a sync_resources row with a non-empty remote_url).
// Returns true if a tombstone was created.
//
// db is a DBTX so callers can pass their own *sql.Tx. The tombstone
// write then commits (or rolls back) atomically with the soft-delete it
// accompanies. Pass *sql.DB only when there is no transaction around it.
func CreateTombstoneIfSynced(ctx context.Context, db DBTX, calendarID int64, uid string) (bool, error) {
	if calendarID == 0 || uid == "" {
		return false, nil
	}
	var remoteURL string
	err := db.QueryRowContext(ctx,
		`SELECT remote_url FROM sync_resources WHERE calendar_id = ? AND uid = ?`,
		calendarID, uid,
	).Scan(&remoteURL)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if remoteURL == "" {
		return false, nil
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (?, ?, ?)
		 ON CONFLICT(calendar_id, uid) DO UPDATE SET
		     remote_url = excluded.remote_url,
		     deleted_at = excluded.deleted_at`,
		calendarID, uid, remoteURL,
	)
	if err != nil {
		return false, err
	}
	return true, nil
}

// moveTables maps a sync owner type to the tables whose rows carry the
// calendar of one UID: the item table first, then the delete logs that
// restore reads. Table names go into SQL text, so only these fixed names
// are allowed.
var moveTables = map[string][]string{
	"event":   {"events", "event_exdate_deletes", "event_truncate_deletes"},
	"todo":    {"todos", "todo_exdate_deletes"},
	"journal": {"journals", "journal_exdate_deletes"},
}

// RecordResourceMove completes a local move of a resource from one calendar
// to another (issue #828). Call it in the transaction of the move, after
// the update of the moved row.
//
// A CalDAV object holds the master and all overrides of one UID, so the
// series moves as one unit. The function moves every other row of the UID
// in the source calendar to the target calendar. This includes the
// soft-deleted rows and the delete-log rows, so a later restore finds them
// in the target calendar.
//
// On the target calendar it clears a pending tombstone for the UID. A move
// back to a calendar then cannot delete the server object of that calendar.
//
// On the source calendar it then updates the sync row:
//   - A sync row with a server object gets a tombstone, so the sync engine
//     deletes that object. The row also loses its dirty flag, so the source
//     push does not upload the moved body again.
//   - A sync row with no server object is deleted. Nothing on the server
//     needs a delete, and a push of that row would create a copy in the
//     source calendar.
func RecordResourceMove(ctx context.Context, db DBTX, ownerType string, fromCalendarID, toCalendarID int64, uid string) error {
	if fromCalendarID == toCalendarID || uid == "" {
		return nil
	}
	tables, ok := moveTables[ownerType]
	if !ok {
		return fmt.Errorf("record resource move: unknown owner type %q", ownerType)
	}
	for _, table := range tables {
		if _, err := db.ExecContext(ctx,
			`UPDATE `+table+` SET calendar_id = ? WHERE calendar_id = ? AND uid = ?`,
			toCalendarID, fromCalendarID, uid,
		); err != nil {
			return fmt.Errorf("move %s rows: %w", table, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM tombstones WHERE calendar_id = ? AND uid = ?`,
		toCalendarID, uid,
	); err != nil {
		return fmt.Errorf("clear target tombstone: %w", err)
	}

	var remoteURL string
	err := db.QueryRowContext(ctx,
		`SELECT remote_url FROM sync_resources WHERE calendar_id = ? AND uid = ?`,
		fromCalendarID, uid,
	).Scan(&remoteURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get source sync resource: %w", err)
	}
	if remoteURL == "" {
		if _, err := db.ExecContext(ctx,
			`DELETE FROM sync_resources WHERE calendar_id = ? AND uid = ?`,
			fromCalendarID, uid,
		); err != nil {
			return fmt.Errorf("delete unpushed source sync resource: %w", err)
		}
		return nil
	}
	if _, err := CreateTombstoneIfSynced(ctx, db, fromCalendarID, uid); err != nil {
		return fmt.Errorf("create source tombstone: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE sync_resources SET dirty = 0 WHERE calendar_id = ? AND uid = ?`,
		fromCalendarID, uid,
	); err != nil {
		return fmt.Errorf("clear source dirty flag: %w", err)
	}
	return nil
}
