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
// dirtyCalendar for the rule. The rule runs before the account check: a
// legacy override in a local calendar can have its master in a synced
// calendar, and that calendar needs the mark.
func MarkResourceDirty(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) error {
	if calendarID == 0 || uid == "" {
		return nil
	}
	calendarID, err := dirtyCalendar(ctx, db, calendarID, uid, ownerType)
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

// LegacyOverride reports whether an override row is a legacy override.
// ownMasterRows is the count of the master rows of its UID, live or in the
// trash, in the calendar of the row.
//
// A legacy override is an override row in a calendar that holds no master
// row of its UID, live or in the trash. An older version saved such a row
// on a calendar change (issue #828). A master in the trash still owns the
// overrides of its calendar: a delete of a series soft-deletes the master
// and the overrides together, and a restore brings them back together.
//
// This is the one definition of the term. dirtyCalendar, uidmove (the move
// rule), and the override lookup of the event service use it.
func LegacyOverride(ownMasterRows int64) bool {
	return ownMasterRows == 0
}

// dirtyCalendar returns the calendar whose sync row a write of a row of uid
// in calendarID must mark dirty.
//
// The export of a UID holds the master and every override, in all
// calendars (issue #842). A dirty mark on the calendar of a legacy override
// (see LegacyOverride) uploads the whole series into it a second time. So
// when calendarID holds an override row of uid (live or in the trash) that
// is a legacy override, and exactly one calendar holds a live master, the
// mark goes to that calendar. Each other case marks calendarID. A master in
// the trash keeps the mark in its calendar: a delete of a series in A must
// not send its mark to a live copy of the UID in B.
//
// The common case (calendarID holds the master) costs one aggregate lookup
// on the UNIQUE(calendar_id, uid, recurrence_id) key. Only the legacy case
// reads the masters of the UID.
func dirtyCalendar(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) (int64, error) {
	q := New(db)
	// own counts the master rows and the override rows of uid in
	// calendarID. masterCalendars counts the calendars with a live master
	// and gives the lowest of them.
	var own, masterCalendars func() (int64, int64, error)
	switch ownerType {
	case "event":
		own = func() (int64, int64, error) {
			r, err := q.CountEventUIDRowsInCalendar(ctx, CountEventUIDRowsInCalendarParams{CalendarID: calendarID, Uid: uid})
			return r.Masters, r.Overrides, err
		}
		masterCalendars = func() (int64, int64, error) {
			r, err := q.EventUIDMasterCalendars(ctx, uid)
			return r.Calendars, r.CalendarID, err
		}
	case "todo":
		own = func() (int64, int64, error) {
			r, err := q.CountTodoUIDRowsInCalendar(ctx, CountTodoUIDRowsInCalendarParams{CalendarID: calendarID, Uid: uid})
			return r.Masters, r.Overrides, err
		}
		masterCalendars = func() (int64, int64, error) {
			r, err := q.TodoUIDMasterCalendars(ctx, uid)
			return r.Calendars, r.CalendarID, err
		}
	case "journal":
		own = func() (int64, int64, error) {
			r, err := q.CountJournalUIDRowsInCalendar(ctx, CountJournalUIDRowsInCalendarParams{CalendarID: calendarID, Uid: uid})
			return r.Masters, r.Overrides, err
		}
		masterCalendars = func() (int64, int64, error) {
			r, err := q.JournalUIDMasterCalendars(ctx, uid)
			return r.Calendars, r.CalendarID, err
		}
	default:
		return calendarID, nil
	}
	ownMasters, ownOverrides, err := own()
	if err != nil {
		return 0, fmt.Errorf("count rows of the UID: %w", err)
	}
	if ownOverrides == 0 || !LegacyOverride(ownMasters) {
		return calendarID, nil
	}
	calendars, first, err := masterCalendars()
	if err != nil {
		return 0, fmt.Errorf("find master calendars: %w", err)
	}
	if calendars == 1 {
		return first, nil
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

// MoveSourceDeleteSafe reports whether a push of fromCalendarID may send
// the source delete of a move of uid to toCalendarID. The delete removes
// the source server object, so it must wait until the target holds the
// item. It is safe when:
//   - fromCalendarID holds no tombstone of uid (the push deletes nothing of
//     the move)
//   - toCalendarID is local (no server copy is expected there)
//   - the sync row of uid in toCalendarID has a server object and no
//     unpushed edit
func MoveSourceDeleteSafe(ctx context.Context, q *Queries, fromCalendarID, toCalendarID int64, uid string) (bool, error) {
	n, err := q.CountTombstonesByCalendarAndUID(ctx, CountTombstonesByCalendarAndUIDParams{CalendarID: fromCalendarID, Uid: uid})
	if err != nil {
		return false, fmt.Errorf("count source tombstones: %w", err)
	}
	if n == 0 {
		return true, nil
	}
	cal, err := q.GetCalendar(ctx, toCalendarID)
	if err != nil {
		return false, fmt.Errorf("get target calendar: %w", err)
	}
	if cal.AccountID == nil || *cal.AccountID == 0 {
		return true, nil
	}
	sr, err := q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: toCalendarID, Uid: uid})
	if found, err := Found(err); !found {
		return false, err
	}
	return sr.Dirty == 0 && sr.RemoteUrl != "", nil
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
