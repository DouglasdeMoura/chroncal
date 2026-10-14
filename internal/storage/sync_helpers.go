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
//
// The mark of a legacy override goes to the calendar of its master. See
// DirtyCalendar for the rule.
func MarkResourceDirty(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) error {
	if calendarID == 0 || uid == "" {
		return nil
	}
	calendarID, err := DirtyCalendar(ctx, db, calendarID, uid, ownerType)
	if err != nil {
		return err
	}
	// Only act if the calendar is linked to an account.
	var accountID *int64
	err = db.QueryRowContext(ctx,
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

// MarkResourceDirty is MarkResourceDirty on the connection of q. A service
// that holds a tx-bound Queries then marks the resource dirty in the same
// transaction.
func (q *Queries) MarkResourceDirty(ctx context.Context, calendarID int64, uid, ownerType string) error {
	return MarkResourceDirty(ctx, q.db, calendarID, uid, ownerType)
}

// ownerTables maps the owner type of a sync resource to the table of its
// rows.
var ownerTables = map[string]string{"event": "events", "todo": "todos", "journal": "journals"}

// DirtyCalendar returns the calendar whose sync row a write of a row of uid
// in calendarID must mark dirty.
//
// The export of a UID holds the master and every override, in all
// calendars (issue #842). A legacy override is an override row that an
// older version saved in a calendar with no master row of the UID. A dirty
// mark on that calendar uploads the whole series into it a second time. So
// when calendarID holds no master row (live or soft-deleted) but holds an
// override row of uid, and exactly one other calendar holds a live master,
// the mark goes to that calendar. Each other case marks calendarID.
//
// The common case (calendarID holds the master) costs one lookup on the
// UNIQUE(calendar_id, uid, recurrence_id) key.
func DirtyCalendar(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) (int64, error) {
	table, ok := ownerTables[ownerType]
	if !ok {
		return calendarID, nil
	}
	var rows int
	// The own master and the own overrides, live or soft-deleted.
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+table+` WHERE calendar_id = ? AND uid = ? AND recurrence_id = ''`, calendarID, uid,
	).Scan(&rows); err != nil {
		return 0, fmt.Errorf("find own master: %w", err)
	}
	if rows > 0 {
		return calendarID, nil
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+table+` WHERE calendar_id = ? AND uid = ? AND recurrence_id != ''`, calendarID, uid,
	).Scan(&rows); err != nil {
		return 0, fmt.Errorf("find own override: %w", err)
	}
	if rows == 0 {
		return calendarID, nil
	}
	dbRows, err := db.QueryContext(ctx,
		`SELECT DISTINCT calendar_id FROM `+table+` WHERE uid = ? AND recurrence_id = '' AND deleted_at IS NULL`, uid)
	if err != nil {
		return 0, fmt.Errorf("find masters: %w", err)
	}
	defer dbRows.Close()
	var masters []int64
	for dbRows.Next() {
		var id int64
		if err := dbRows.Scan(&id); err != nil {
			return 0, fmt.Errorf("find masters: %w", err)
		}
		masters = append(masters, id)
	}
	if err := dbRows.Err(); err != nil {
		return 0, fmt.Errorf("find masters: %w", err)
	}
	if len(masters) == 1 {
		return masters[0], nil
	}
	return calendarID, nil
}

// ErrMoveOpenConflict reports a move of a UID that has an open sync conflict
// in the source calendar or in the target calendar. In the source, the move
// would clear the dirty flag, and a later pass would resolve the conflict
// for the server. In the target, the move would mark the UID dirty over
// the conflict. Either way the recorded local body would be lost. Resolve
// the conflict first.
var ErrMoveOpenConflict = errors.New("the item has an open sync conflict; resolve the sync conflict first (chroncal sync conflicts, then chroncal sync resolve <id>)")

// RecordResourceMove completes a local move of a resource from one calendar
// to another (issue #828). Call it in the transaction of the move, after
// the update of the moved row. q must be bound to that transaction.
//
// A CalDAV object holds the master and all overrides of one UID, so the
// series moves as one unit. moveRows moves every other row of the UID in
// the source calendar to the target calendar. Each domain binds its own
// queries into moveRows. The rows include the soft-deleted rows and the
// delete-log rows, so a later restore finds them in the target calendar.
//
// A UID with an open sync conflict in the source calendar or in the target
// calendar does not move. RecordResourceMove then returns
// ErrMoveOpenConflict, and the caller rolls the transaction back. A
// conflict in the target holds a recorded local body, and the move would
// mark the target dirty over it.
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
func RecordResourceMove(ctx context.Context, q *Queries, fromCalendarID, toCalendarID int64, uid string, moveRows func(context.Context) error) error {
	if fromCalendarID == toCalendarID || uid == "" {
		return nil
	}
	for _, calendarID := range []int64{fromCalendarID, toCalendarID} {
		open, err := q.CountOpenSyncConflicts(ctx, CountOpenSyncConflictsParams{CalendarID: calendarID, Uid: uid})
		if err != nil {
			return fmt.Errorf("check open conflict: %w", err)
		}
		if open > 0 {
			return fmt.Errorf("move %s: %w", uid, ErrMoveOpenConflict)
		}
	}
	if err := moveRows(ctx); err != nil {
		return fmt.Errorf("move rows: %w", err)
	}
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, DeleteTombstonesByCalendarAndUIDParams{
		CalendarID: toCalendarID, Uid: uid,
	}); err != nil {
		return fmt.Errorf("clear target tombstone: %w", err)
	}
	if _, err := CreateTombstoneIfSynced(ctx, q.db, fromCalendarID, uid); err != nil {
		return fmt.Errorf("create source tombstone: %w", err)
	}
	if err := q.ClearPushedSyncResourceDirty(ctx, ClearPushedSyncResourceDirtyParams{
		CalendarID: fromCalendarID, Uid: uid,
	}); err != nil {
		return fmt.Errorf("clear source dirty flag: %w", err)
	}
	if err := q.DeleteUnpushedSyncResource(ctx, DeleteUnpushedSyncResourceParams{
		CalendarID: fromCalendarID, Uid: uid,
	}); err != nil {
		return fmt.Errorf("delete unpushed source sync resource: %w", err)
	}
	return nil
}

// DeleteSyncHrefRetriesByCalendar deletes the two per-href retry tables of
// one calendar: sync_pending_hrefs and sync_known_misses. A calendar reset
// and a remote unlink call it, so a new server state starts with no old
// retry budget.
func (q *Queries) DeleteSyncHrefRetriesByCalendar(ctx context.Context, calendarID int64) error {
	if err := q.DeleteSyncPendingHrefsByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete pending hrefs: %w", err)
	}
	if err := q.DeleteSyncKnownMissesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete known misses: %w", err)
	}
	return nil
}
