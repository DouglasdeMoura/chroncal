package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	return markResourceDirty(ctx, db, calendarID, uid, ownerType, 0)
}

// MarkEventRowDirty is MarkResourceDirty for an edit of the event row rowID
// of uid in calendarID. When no series owns the row (NoSeriesOwnerMsg), only
// that row gets the edit_unowned mark.
func MarkEventRowDirty(ctx context.Context, db DBTX, calendarID int64, uid string, rowID int64) error {
	if calendarID == 0 || uid == "" {
		return nil
	}
	return markResourceDirty(ctx, db, calendarID, uid, "event", rowID)
}

// markResourceDirty is the body of MarkResourceDirty. rowID names the edited
// event row, or 0 when the caller knows no row. With no row, an edit with no
// series owner marks every live override row of uid in calendarID.
func markResourceDirty(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string, rowID int64) error {
	target, err := dirtyCalendar(ctx, db, calendarID, uid, ownerType)
	if err != nil {
		return err
	}
	if target == unownedCalendar {
		return recordUnownedEdit(ctx, db, calendarID, uid, rowID)
	}
	if err := clearUnownedEdit(ctx, db, calendarID, uid, ownerType); err != nil {
		return err
	}
	if target == 0 {
		return nil
	}
	return markCalendarDirty(ctx, db, target, uid, ownerType)
}

// NoSeriesOwnerMsg tells the user why an edit stays local: no series owns
// the legacy override, because two calendars hold a live master of its UID.
// No export holds the row, so a dirty mark would drop the edit in silence.
// MarkResourceDirty sets edit_unowned on the row instead, and the sync status
// shows this text.
const NoSeriesOwnerMsg = "the item belongs to no series because two calendars hold its master; use the row ID of one series"

// unownedCalendar is the result of dirtyCalendar for a legacy override that
// no series owns (NoSeriesOwnerMsg).
const unownedCalendar = -1

// recordUnownedEdit sets edit_unowned on the edited row rowID. With rowID 0
// (a caller that knows no row), it marks the live override rows of uid in
// calendarID. No calendar gets a dirty mark, so no push drops the edit. The
// edit stays local, and the sync status lists the UID. The mark lives on the
// event rows, so the edit makes no sync row.
func recordUnownedEdit(ctx context.Context, db DBTX, calendarID int64, uid string, rowID int64) error {
	query := `UPDATE events SET edit_unowned = 1
		 WHERE calendar_id = ? AND uid = ? AND recurrence_id != '' AND deleted_at IS NULL`
	args := []any{calendarID, uid}
	if rowID != 0 {
		query += ` AND id = ?`
		args = append(args, rowID)
	}
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("record the edit with no series: %w", err)
	}
	return nil
}

// clearUnownedEdit clears edit_unowned on the rows of uid in calendarID. A
// later edit with an owner, or with its own master, makes the mark stale.
// Only an event row can carry the mark. The update writes only a marked row:
// for any other edit it is one lookup on the unique key of the event rows.
func clearUnownedEdit(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) error {
	if ownerType != "event" {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE events SET edit_unowned = 0
		 WHERE calendar_id = ? AND uid = ? AND edit_unowned = 1`,
		calendarID, uid,
	); err != nil {
		return fmt.Errorf("clear the edit with no series: %w", err)
	}
	return nil
}

// ReconcileAfterRestore resets the sync state of uid in calendarID after a
// restore. It drops the unsent tombstone of the calendar and marks the
// resource dirty (see MarkResourceDirty). The two steps go together: the
// tombstone goes only when the mark goes to calendarID itself. When
// dirtyCalendar names no calendar, or another calendar, the tombstone stays.
// The rows then stay local until their master comes back, and the server
// object still gets its delete. A restore never leaves a dropped tombstone
// with no dirty mark.
func ReconcileAfterRestore(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) error {
	if calendarID == 0 || uid == "" {
		return nil
	}
	target, err := dirtyCalendar(ctx, db, calendarID, uid, ownerType)
	if err != nil {
		return err
	}
	if target == unownedCalendar {
		// No series owns the restored row. The tombstone stays. A restore
		// names no edited row, so it marks no row: the mark of a row
		// names an edit of that row.
		return nil
	}
	if err := clearUnownedEdit(ctx, db, calendarID, uid, ownerType); err != nil {
		return err
	}
	if target == 0 {
		return nil
	}
	// The mark of a legacy override goes to the calendar of its master. The
	// tombstone of the calendar of the row then stays: the server object
	// there still needs its delete.
	if target == calendarID {
		// A gone source also drops its href (see ClearTombstonesOfUID).
		if err := ClearTombstonesOfUID(ctx, New(db), calendarID, uid); err != nil {
			return fmt.Errorf("clear tombstone after restore: %w", err)
		}
	}
	return markCalendarDirty(ctx, db, target, uid, ownerType)
}

// ReconcileAfterRestore is ReconcileAfterRestore on the connection of q. A
// restore with a tx-bound Queries then commits the restore, the tombstone
// delete, and the dirty mark together.
func (q *Queries) ReconcileAfterRestore(ctx context.Context, calendarID int64, uid, ownerType string) error {
	return ReconcileAfterRestore(ctx, q.db, calendarID, uid, ownerType)
}

// markCalendarDirty marks uid dirty in calendarID when the calendar has an
// account. A local calendar gets no sync row.
func markCalendarDirty(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) error {
	// Only act if the calendar is linked to an account.
	linked, err := calendarHasAccount(ctx, db, calendarID)
	if err != nil || !linked {
		return err
	}
	// The write bumps rev on every edit. A concurrent push captured the
	// prior rev before it exported the body. It then refuses to clear dirty,
	// so it cannot drop this edit. See FinalizePushedResource and issue #92.
	// One statement holds the write (UpsertDirtySyncResourceOfCalendar).
	err = New(db).UpsertDirtySyncResourceOfCalendar(ctx, UpsertDirtySyncResourceOfCalendarParams{
		CalendarID: calendarID, Uid: uid, OwnerType: ownerType,
	})
	return err
}

// calendarHasAccount reports whether an account links calendarID. A
// local calendar, or no calendar, gives false.
func calendarHasAccount(ctx context.Context, db DBTX, calendarID int64) (bool, error) {
	var accountID *int64
	err := db.QueryRowContext(ctx,
		`SELECT account_id FROM calendars WHERE id = ?`, calendarID,
	).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return accountID != nil && *accountID != 0, nil
}

// A path that creates a UID again after a delete clears the tombstone of the
// UID (the ical import, see icaltransfer.Import). The tombstone pass must
// not delete the server object under the live rows (issue #841). The restore
// paths clear the tombstone on their own too. UpsertByUID and
// MarkResourceDirty do not touch tombstones. The sync pull persists through
// them, and a delete that lands during a pull keeps its pending server
// DELETE.

// CreateTombstoneIfSynced inserts a tombstone row if the resource was
// previously synced (has a sync_resources row with a non-empty remote_url).
// Returns true if a tombstone was created.
//
// A sync row without a server object needs no tombstone. The function
// deletes that row instead. The local rows of the UID are gone, so a push
// of the row finds nothing. A move that waits for the row waits without
// end (issue #841). A restore marks the UID dirty again.
//
// db is a DBTX so callers can pass their own *sql.Tx. The tombstone
// write then commits (or rolls back) atomically with the soft-delete it
// accompanies. Pass *sql.DB only when there is no transaction around it.
func CreateTombstoneIfSynced(ctx context.Context, db DBTX, calendarID int64, uid string) (bool, error) {
	return createTombstoneIfSynced(ctx, db, calendarID, uid, nil)
}

// CreateMoveTombstoneIfSynced is CreateTombstoneIfSynced for the source
// of a move into movedTo. The local pick of a move conflict calls it when
// the source lost its tombstone, so the source DELETE goes out again
// (issue #841).
func CreateMoveTombstoneIfSynced(ctx context.Context, db DBTX, calendarID int64, uid string, movedTo int64) (bool, error) {
	return createTombstoneIfSynced(ctx, db, calendarID, uid, &movedTo)
}

// createTombstoneIfSynced is CreateTombstoneIfSynced with the target
// calendar of a move. movedTo is nil for a plain delete.
func createTombstoneIfSynced(ctx context.Context, db DBTX, calendarID int64, uid string, movedTo *int64) (bool, error) {
	if calendarID == 0 || uid == "" {
		return false, nil
	}
	q := New(db)
	sr, err := q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	if found, err := Found(err); !found {
		return false, err
	}
	if sr.RemoteUrl == "" {
		// The rev guard keeps a row that an edit or a restore marked
		// after the read. A push that finalizes after this delete finds no
		// row and writes a tombstone for the new href (issue #841).
		if _, err := q.DeleteUnpushedSyncResource(ctx, DeleteUnpushedSyncResourceParams{CalendarID: calendarID, Uid: uid, Rev: sr.Rev}); err != nil || movedTo != nil {
			return false, err
		}
		// A delete of the item, whatever the rev guard kept (a row that a
		// later mark keeps goes in the push: see CloseConflictsOfGoneItem).
		return false, CloseConflictsOfGoneItem(ctx, q, calendarID, uid)
	}
	if err := q.CreateTombstone(ctx, CreateTombstoneParams{
		CalendarID: calendarID, Uid: uid, RemoteUrl: sr.RemoteUrl, MovedToCalendarID: movedTo,
	}); err != nil {
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
// rule), and the override lookup of the event service use it. The series
// that owns a legacy override is the one that uidmove.LegacyOwner names.
func LegacyOverride(ownMasterRows int64) bool {
	return ownMasterRows == 0
}

// dirtyCalendar returns the calendar whose sync row a write of a row of uid
// in calendarID must mark dirty. 0 means no calendar gets the mark.
// unownedCalendar means that no series owns the row (NoSeriesOwnerMsg).
//
// The export of a UID in one calendar holds the master of that calendar, its
// overrides, and the legacy overrides that its series owns. See
// LegacyOverride and the view event_series_rows (issue #842). A calendar with
// a legacy override and no master exports nothing of it, so the mark moves.
// A calendar with its own event master in the trash gets no mark. Its rows
// stay local until the master comes back. A pull can bring back a live row
// there, and an edit of that row marks nothing too, by design:
//   - The series delete queued a tombstone, so the next push deletes the
//     series object. The server must not hold the row then.
//   - The export refuses a body with no master. A mark would only make the
//     push clear it (errResourceMissing in the sync engine), and the edit
//     would go nowhere.
//   - The restore of the series marks the UID (ReconcileAfterRestore). The
//     next push then sends the series with each edit of its rows.
//
// For a legacy override of an event, the owner rule of the view decides:
//   - A live owner master gets the mark in its calendar.
//   - An owner master in the trash gets no mark. The rows stay local until
//     the master comes back, as the export rule of sync requires.
//   - With no owner and two live masters of the UID, no calendar gets the
//     mark: the result is unownedCalendar (NoSeriesOwnerMsg).
//   - With no owner and no live master, calendarID gets the mark.
//
// For a todo or a journal, the one calendar with a live master gets the
// mark. Else calendarID gets it.
//
// The common case (calendarID holds the master) costs one aggregate lookup
// on the UNIQUE(calendar_id, uid, recurrence_id) key. Only the legacy case
// reads the owner.
func dirtyCalendar(ctx context.Context, db DBTX, calendarID int64, uid, ownerType string) (int64, error) {
	q := New(db)
	// own counts the master rows and the override rows of uid in
	// calendarID. owner gives the calendar of a legacy override.
	var own func() (int64, int64, error)
	var owner func() (int64, error)
	// ownTrashed reports an event master of calendarID in the trash, with
	// no live master beside it.
	ownTrashed := false
	oneLiveMaster := func(calendars, first int64) int64 {
		if calendars == 1 {
			return first
		}
		return calendarID
	}
	switch ownerType {
	case "event":
		own = func() (int64, int64, error) {
			r, err := q.CountEventUIDRowsInCalendar(ctx, CountEventUIDRowsInCalendarParams{CalendarID: calendarID, Uid: uid, IncludeDeleted: true})
			ownTrashed = r.Masters > 0 && r.LiveMasters == 0
			return r.Masters, r.Overrides, err
		}
		owner = func() (int64, error) {
			m, err := q.GetSeriesMasterOfCalendar(ctx, GetSeriesMasterOfCalendarParams{CalendarID: calendarID, Uid: uid, IncludeDeleted: true})
			found, err := Found(err)
			switch {
			case err != nil:
				return 0, err
			case !found:
				// No series owns the row. With two live masters of the
				// UID, no export holds it (NoSeriesOwnerMsg).
				live, err := q.EventUIDMasterCalendars(ctx, uid)
				if err != nil {
					return 0, err
				}
				if live.Calendars >= 2 {
					return unownedCalendar, nil
				}
				return calendarID, nil
			case m.DeletedAt != nil:
				return 0, nil
			}
			return m.CalendarID, nil
		}
	case "todo":
		own = func() (int64, int64, error) {
			r, err := q.CountTodoUIDRowsInCalendar(ctx, CountTodoUIDRowsInCalendarParams{CalendarID: calendarID, Uid: uid})
			return r.Masters, r.Overrides, err
		}
		owner = func() (int64, error) {
			r, err := q.TodoUIDMasterCalendars(ctx, uid)
			return oneLiveMaster(r.Calendars, r.CalendarID), err
		}
	case "journal":
		own = func() (int64, int64, error) {
			r, err := q.CountJournalUIDRowsInCalendar(ctx, CountJournalUIDRowsInCalendarParams{CalendarID: calendarID, Uid: uid})
			return r.Masters, r.Overrides, err
		}
		owner = func() (int64, error) {
			r, err := q.JournalUIDMasterCalendars(ctx, uid)
			return oneLiveMaster(r.Calendars, r.CalendarID), err
		}
	default:
		return calendarID, nil
	}
	ownMasters, ownOverrides, err := own()
	if err != nil {
		return 0, fmt.Errorf("count rows of the UID: %w", err)
	}
	if ownTrashed {
		return 0, nil
	}
	if ownOverrides == 0 || !LegacyOverride(ownMasters) {
		return calendarID, nil
	}
	target, err := owner()
	if err != nil {
		return 0, fmt.Errorf("find the owner calendar: %w", err)
	}
	return target, nil
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
// On the target calendar it clears an unsent tombstone of the UID. A move
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
	return recordResourceMove(ctx, q, fromCalendarID, toCalendarID, uid, &toCalendarID, moveRows, nil)
}

// RecordResourceMoveBack is RecordResourceMove for the cancel of a move
// (issue #841). The rows go back from the target to the source. The
// tombstone of a landed target copy names no move target. The cleanup delete
// of that copy follows the plain delete-vs-edit rule, and a 412 on it
// records no reversed move conflict.
func RecordResourceMoveBack(ctx context.Context, q *Queries, fromCalendarID, toCalendarID int64, uid string, moveRows func(context.Context) error, liveInDestination func(context.Context) (int64, error)) error {
	return recordResourceMove(ctx, q, fromCalendarID, toCalendarID, uid, nil, moveRows, liveInDestination)
}

func recordResourceMove(ctx context.Context, q *Queries, fromCalendarID, toCalendarID int64, uid string, movedTo *int64, moveRows func(context.Context) error, liveInDestination func(context.Context) (int64, error)) error {
	if fromCalendarID == toCalendarID || uid == "" {
		return nil
	}
	// The guards of checkMoveGuards: an open sync conflict of the UID in
	// either calendar, and for a new move an open move conflict of the UID
	// in the accounts of the move. Such a conflict waits for the user. A
	// new move would run over its rows (issue #841).
	if kind, _, err := checkMoveGuards(ctx, q, fromCalendarID, toCalendarID, uid, movedTo != nil); err != nil {
		return err
	} else if kind != "" {
		return fmt.Errorf("move %s: %w", uid, ErrMoveOpenConflict)
	}
	if err := moveRows(ctx); err != nil {
		return fmt.Errorf("move rows: %w", err)
	}
	// A new move of the UID starts a new history. A resolved move conflict row
	// of the UID must not restore an old body beside the moved item (issue
	// #841). Such a row is a cancelled or finished move of an earlier time, on
	// any calendar. The move back of a cancel keeps its own row.
	if movedTo != nil {
		if err := q.DeleteResolvedMoveConflictsOfUID(ctx, DeleteResolvedMoveConflictsOfUIDParams{
			Uid: uid, FromCalendarID: fromCalendarID, ToCalendarID: toCalendarID,
		}); err != nil {
			return fmt.Errorf("delete stale move conflicts: %w", err)
		}
	}
	if err := ClearTombstonesOfUID(ctx, q, toCalendarID, uid); err != nil {
		return fmt.Errorf("clear target tombstone: %w", err)
	}
	// A chain move (A to B, then B to C) leaves the tombstone of A with
	// the target B. Point it at C, so the source delete of A waits for C
	// and a refused create in C finds it (issue #841).
	//
	// A move back follows the same rule, toward the destination D of the
	// move back. A tombstone that named the old target is a pending delete
	// of an old copy, and the item lives in D now. With the target D, a 412
	// on that delete records a move conflict on D. The local pick deletes
	// the old object, and the server pick moves the rows of D there. Either
	// way one copy stays, and no version is lost. The plain rule would keep
	// the old object and drop the tombstone, so the next pull would bring
	// a second copy. The count runs after moveRows, so it asks whether the
	// moved rows are live in D: the item lives in D after the move back.
	// Only when they are not does the tombstone become a plain delete:
	// nothing in D needs the protection then. liveInDestination counts in
	// the table of the owner type of the rows.
	retarget := movedTo != nil
	if !retarget && liveInDestination != nil {
		live, err := liveInDestination(ctx)
		if err != nil {
			return fmt.Errorf("count the live rows of %s: %w", uid, err)
		}
		retarget = live > 0
	}
	if retarget {
		if err := q.RetargetMoveTombstones(ctx, RetargetMoveTombstonesParams{
			ToCalendarID: &toCalendarID, Uid: uid, FromCalendarID: &fromCalendarID,
		}); err != nil {
			return fmt.Errorf("retarget move tombstones: %w", err)
		}
	} else if err := q.ClearMoveTargetOfTombstones(ctx, ClearMoveTargetOfTombstonesParams{Uid: uid, CalendarID: &fromCalendarID}); err != nil {
		return fmt.Errorf("end the move tombstones of %s: %w", uid, err)
	}
	// The tombstone names the target calendar, so the tombstone pass can
	// tell the move apart from a delete (issue #841). A source row without
	// a server object goes instead of a tombstone.
	if _, err := createTombstoneIfSynced(ctx, q.db, fromCalendarID, uid, movedTo); err != nil {
		return fmt.Errorf("create source tombstone: %w", err)
	}
	if err := q.ClearPushedSyncResourceDirty(ctx, ClearPushedSyncResourceDirtyParams{
		CalendarID: fromCalendarID, Uid: uid,
	}); err != nil {
		return fmt.Errorf("clear source dirty flag: %w", err)
	}
	return nil
}

// MoveSourceDeleteSafe reports whether a push of fromCalendarID may send
// the source delete of a move of uid. The delete removes the source server
// object, so it must wait until the target holds the item. It is safe
// when no tombstone of uid in fromCalendarID is in the view
// move_wait_tombstones. A local target has no sync row, and a delete of
// the moved copy removes its unpushed row (CreateTombstoneIfSynced), so
// those cases do not wait. The dirty flag plays no part: a push clears
// the flag of a row whose local rows are gone without a PUT. See issue
// #841.
func MoveSourceDeleteSafe(ctx context.Context, q *Queries, fromCalendarID int64, uid string) (bool, error) {
	n, err := q.CountMoveWaitTombstones(ctx, CountMoveWaitTombstonesParams{CalendarID: fromCalendarID, Uid: uid})
	if err != nil {
		return false, fmt.Errorf("count move wait tombstones: %w", err)
	}
	return n == 0, nil
}

// MoveDeleteWaitUIDs returns the UIDs whose tombstone in calendarID must
// wait for a calendar move (the view move_wait_tombstones). The tombstone
// pass reads it once per pass. The query DeleteStaleTombstones keeps the
// same rows. See issue #841.
func MoveDeleteWaitUIDs(ctx context.Context, q *Queries, calendarID int64) (map[string]bool, error) {
	uids, err := q.ListMoveDeleteWaitUIDs(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list move delete waits: %w", err)
	}
	out := make(map[string]bool, len(uids))
	for _, uid := range uids {
		out[uid] = true
	}
	return out, nil
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

// CloseConflictsOfGoneItem settles the conflicts of an unpushed item that
// the user deleted in calendarID. An open conflict of the gone row has no
// local side any more, and a move into this calendar has nothing to create.
// The edit conflict closes. An open move conflict, and a local-picked one,
// close as abandoned. The move tombstone that names this calendar keeps its
// target. Without the target row it does not wait, and a 412 on its delete
// follows the plain rule (moveTarget finds no row). A restore of the copy
// then marks the row again, and the move goes on as before the delete. The
// delete of the item and the push that deletes a row without local rows both
// call it (issue #841).
func CloseConflictsOfGoneItem(ctx context.Context, q *Queries, calendarID int64, uid string) error {
	if err := q.CloseOpenEditConflict(ctx, CloseOpenEditConflictParams{CalendarID: calendarID, Uid: uid}); err != nil {
		return err
	}
	return q.CloseMoveConflict(ctx, CloseMoveConflictParams{CalendarID: calendarID, Uid: uid})
}

// ErrPendingMove reports an import of a UID whose source calendar still
// holds the move tombstone of that UID. The move waits for its source
// delete. An import of a new copy there loses the server object under the
// new rows. For a todo or a journal, whose key spans calendars, the import
// moves the row back. The import keeps the move (issue #841).
var ErrPendingMove = errors.New("the item waits for its move")

// ErrCrossCalendarImport reports an import of a todo or a journal whose
// row lives in another synced calendar. The key of these domains spans
// calendars, so the upsert would move the row with no move record. The
// server then holds two copies (issue #841).
var ErrCrossCalendarImport = errors.New("the item lives in another calendar")

// ErrUIDInOtherCalendar reports a write of a todo or a journal into one
// calendar while a row of its UID lives in another calendar, live or in the
// trash. The key of these domains spans calendars, so the upsert would
// write the row of the other calendar. The upsert never moves a row: a
// move uses the rules of uidmove (issue #911).
var ErrUIDInOtherCalendar = errors.New("the UID lives in another calendar")

// CheckUIDInOneCalendar returns ErrUIDInOtherCalendar when a row of uid
// lives outside calendarID. outside reads the calendar of such a row in
// the transaction of q, or returns sql.ErrNoRows. ownerType is "todo" or
// "journal".
func CheckUIDInOneCalendar(ctx context.Context, q *Queries, ownerType, uid string, calendarID int64, outside func(context.Context) (int64, error)) error {
	other, err := outside(ctx)
	found, err := Found(err)
	if err != nil {
		return fmt.Errorf("check the calendars of %s: %w", uid, err)
	}
	if !found {
		return nil
	}
	return fmt.Errorf("%w: %s %s lives in calendar %q, not in calendar %q; a %s UID cannot be in two calendars",
		ErrUIDInOtherCalendar, ownerType, uid, CalendarLabel(ctx, q, other), CalendarLabel(ctx, q, calendarID), ownerType)
}

// ClearTombstonesOfUID deletes the tombstones of uid in calendarID through
// q. A tombstone with the source_gone mark names a server object that is
// gone. So the sync row first drops its href and its etag, and the next
// push creates the item again (issue #841). Pass the queries of a
// transaction, or use ClearTombstonesOfUIDInTx.
func ClearTombstonesOfUID(ctx context.Context, q *Queries, calendarID int64, uid string) error {
	if err := q.ClearHrefOfGoneSource(ctx, ClearHrefOfGoneSourceParams{CalendarID: calendarID, Uid: uid}); err != nil {
		return fmt.Errorf("clear the href of a gone source: %w", err)
	}
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, DeleteTombstonesByCalendarAndUIDParams{CalendarID: calendarID, Uid: uid}); err != nil {
		return fmt.Errorf("delete the tombstones: %w", err)
	}
	return nil
}

// ClearTombstonesOfUIDInTx runs ClearTombstonesOfUID in one transaction of
// db, so the href and the tombstone go together.
func ClearTombstonesOfUIDInTx(ctx context.Context, db *sql.DB, q *Queries, calendarID int64, uid string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := ClearTombstonesOfUID(ctx, q.WithTx(tx), calendarID, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// CalendarLabel returns the name of calendarID. When the lookup fails,
// it returns the ID as text.
func CalendarLabel(ctx context.Context, q *Queries, calendarID int64) string {
	if cal, err := q.GetCalendar(ctx, calendarID); err == nil {
		return cal.Name
	}
	return fmt.Sprint(calendarID)
}

// pendingMoveError returns ErrPendingMove with the name of the target
// calendar of the move.
func pendingMoveError(ctx context.Context, q *Queries, uid string, targetID int64) error {
	return fmt.Errorf("%w: %s waits for its move to calendar %q; the import keeps the move", ErrPendingMove, uid, CalendarLabel(ctx, q, targetID))
}

// CheckNoCrossCalendarMove refuses an import upsert of a row of uid and
// recurrenceID into calendarID in one case. The prior row sits in another
// calendar, and either calendar takes part in a pending move of the UID. The
// key of a todo and of a journal spans calendars, so the upsert would move
// that row. existingCalendarID is the calendar of the prior row, or 0 when
// no row exists (issue #841).
func CheckNoCrossCalendarMove(ctx context.Context, q *Queries, calendarID, existingCalendarID int64, uid string) error {
	if existingCalendarID == 0 || existingCalendarID == calendarID {
		return nil
	}
	for _, id := range []int64{existingCalendarID, calendarID} {
		n, err := q.CountPendingMovesOfUIDTouchingCalendar(ctx, CountPendingMovesOfUIDTouchingCalendarParams{Uid: uid, CalendarID: id})
		if err != nil {
			return fmt.Errorf("check the pending moves of %s: %w", uid, err)
		}
		if n > 0 {
			return fmt.Errorf("%w: %s waits for its move, and its row sits in calendar %q; the import does not move the row", ErrPendingMove, uid, CalendarLabel(ctx, q, existingCalendarID))
		}
	}
	return nil
}

// Import is the state of one ical import that every row shares: the sync
// state of the import calendar, read once. Each rule reads its rows inside
// the transaction of the row, so a write of another writer between two
// rows is never missed (issue #841).
type Import struct {
	// Synced tells whether the import calendar is synced.
	Synced bool
}

// NewImport returns the state of an import into a calendar. synced tells
// whether that calendar is synced.
func NewImport(synced bool) *Import {
	return &Import{Synced: synced}
}

// importTombstone reads the tombstone of uid in calendarID, in the
// transaction of q. One indexed point query.
func importTombstone(ctx context.Context, q *Queries, calendarID int64, uid string) (Tombstone, bool, error) {
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: calendarID, Uid: uid})
	found, err := Found(err)
	return ts, found, err
}

// importRow names one imported row for the import rules.
type importRow struct {
	// Import is the state of the import that the row belongs to.
	Import *Import
	// CalendarID is the calendar of the import.
	CalendarID int64
	// UID and RecurrenceID name the row. An empty RecurrenceID names a
	// master or a standalone item.
	UID          string
	RecurrenceID string
	// OwnerType is the sync owner type of the row ("event", "todo", or
	// "journal").
	OwnerType string
	// ExistingCalendarID is the calendar of the row before the upsert, or
	// 0 when no row existed. Only a key that spans calendars uses it.
	ExistingCalendarID int64
	// ExistingTrashed is true when that row was in the trash.
	ExistingTrashed bool
	// tombstoneFound is true when the import calendar holds a plain
	// tombstone of the UID. PrepareImport reads it.
	tombstoneFound bool
}

// PriorLookup returns the row of the imported UID and recurrence ID
// before the upsert: its calendar, and whether it is in the trash. found is
// false when no row exists. A domain whose key spans calendars passes it to
// PrepareImport.
type PriorLookup func(ctx context.Context) (calendarID int64, trashed, found bool, err error)

// PrepareImport applies the import rules that come before the upsert, and
// it returns the row for ApplyImport. Call it in the transaction of the
// upsert, before the upsert, so a refused row writes nothing: no row and no
// category (issue #841).
//
// The rules:
//   - The import refuses a UID whose move tombstone waits in the calendar
//     (ErrPendingMove). This applies to a master and to an override.
//   - prior is not nil for a key that spans calendars (a todo or a
//     journal). The import then refuses an upsert that moves a row of a
//     pending move to another calendar.
//   - It also refuses an upsert that moves a row out of another synced
//     calendar (ErrCrossCalendarImport), live or in the trash. A row from
//     a local calendar moves. The import never writes into another
//     calendar.
func PrepareImport(ctx context.Context, q *Queries, im *Import, calendarID int64, uid, recurrenceID, ownerType string, prior PriorLookup) (PreparedImport, error) {
	row, err := prepareImport(ctx, q, im, calendarID, uid, recurrenceID, ownerType, prior)
	if err != nil {
		return PreparedImport{}, err
	}
	return PreparedImport{row: row, ok: true}, nil
}

// PreparedImport is the token that PrepareImport returns and ApplyImport
// requires. Its fields are unexported, so only PrepareImport makes a valid
// one, and ApplyImport cannot run without the rules before the upsert.
type PreparedImport struct {
	row importRow
	ok  bool
}

func prepareImport(ctx context.Context, q *Queries, im *Import, calendarID int64, uid, recurrenceID, ownerType string, prior PriorLookup) (importRow, error) {
	row := importRow{Import: im, CalendarID: calendarID, UID: uid, RecurrenceID: recurrenceID, OwnerType: ownerType}
	if im.Synced {
		ts, found, err := importTombstone(ctx, q, calendarID, uid)
		if err != nil {
			return row, fmt.Errorf("check the tombstone of %s: %w", uid, err)
		}
		if found && ts.MovedToCalendarID != nil {
			return row, pendingMoveError(ctx, q, uid, *ts.MovedToCalendarID)
		}
		// The read runs in the transaction of the row, so ApplyImport uses
		// it after the upsert and does not read the tombstone again.
		row.tombstoneFound = found
	}
	if prior == nil {
		return row, nil
	}
	priorCalendar, trashed, found, err := prior(ctx)
	if err != nil {
		return row, fmt.Errorf("find the row of %s: %w", uid, err)
	}
	if found {
		row.ExistingCalendarID, row.ExistingTrashed = priorCalendar, trashed
	}
	if err := CheckNoCrossCalendarMove(ctx, q, calendarID, row.ExistingCalendarID, uid); err != nil {
		return row, err
	}
	return row, checkNoSyncedCrossCalendarUpsert(ctx, q, row)
}

// ApplyImport applies the import rules of issue #841 to one upserted row,
// the token that PrepareImport returned. Call it in the transaction of the
// upsert, after the upsert. An error rolls the upsert back. Only a synced
// import calendar (p.Import.Synced) has these rules.
//
// The rules:
//   - A master clears the plain tombstone of its UID. The server object
//     then stays under the new rows. An override alone does not bring
//     back a deleted series, so it keeps the tombstone.
//   - The row gets a dirty mark, so the push sends the imported body. This
//     matches master, where the child replacements of the import mark
//     every item. An override of a deleted series stays local: its series
//     keeps the tombstone, so it gets no mark.
//
// The sync pull does not call it. A delete that lands during a pull then
// keeps its tombstone.
func ApplyImport(ctx context.Context, q *Queries, prepared PreparedImport) error {
	if !prepared.ok {
		return errors.New("apply an import without PrepareImport")
	}
	p := prepared.row
	if !p.Import.Synced {
		// A local calendar has no tombstones and no dirty marks.
		return nil
	}
	// PrepareImport read the tombstone in this transaction and refused a
	// move tombstone, so a found tombstone here is plain.
	found := p.tombstoneFound
	if found && p.RecurrenceID == "" {
		// The tombstone is plain, so the clear deletes it. The pending
		// server DELETE goes, and the row comes back. A tombstone with the
		// source_gone mark names a server object that is gone. So the row
		// drops its href first, and the push creates the item again.
		if err := q.ClearHrefOfGoneSource(ctx, ClearHrefOfGoneSourceParams{CalendarID: p.CalendarID, Uid: p.UID, PlainOnly: 1}); err != nil {
			return fmt.Errorf("clear the href of a gone source: %w", err)
		}
		if err := q.DeletePlainTombstoneOfUID(ctx, DeletePlainTombstoneOfUIDParams{CalendarID: p.CalendarID, Uid: p.UID}); err != nil {
			return fmt.Errorf("clear the pending delete: %w", err)
		}
		found = false
	}
	if found {
		// An override keeps the tombstone of its series, so it stays local.
		return nil
	}
	// The dirty mark sends the imported body. Every import marks the row,
	// with or without child rows, as the child replacements of master do.
	if err := q.MarkResourceDirty(ctx, p.CalendarID, p.UID, p.OwnerType); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return nil
}

// checkNoSyncedCrossCalendarUpsert refuses the import upsert of p when the
// prior row lives in another synced calendar (ErrCrossCalendarImport),
// live or in the trash. The import holds no lock of that calendar, so it
// never writes there. The user restores or purges the row there.
func checkNoSyncedCrossCalendarUpsert(ctx context.Context, q *Queries, p importRow) error {
	if p.ExistingCalendarID == 0 || p.ExistingCalendarID == p.CalendarID {
		return nil
	}
	cal, err := q.GetCalendar(ctx, p.ExistingCalendarID)
	if err != nil {
		return fmt.Errorf("get the calendar of %s: %w", p.UID, err)
	}
	if !CalendarLinked(cal) {
		return nil
	}
	if !p.ExistingTrashed {
		return fmt.Errorf("%w: %s lives in calendar %q; move it there or delete it first", ErrCrossCalendarImport, p.UID, cal.Name)
	}
	return fmt.Errorf("%w: %s is in the trash of calendar %q; restore it there or purge it first", ErrCrossCalendarImport, p.UID, cal.Name)
}

// CalendarReadOnly reports whether a calendars.remote_access value marks a
// read-only server collection. calendaraccess.IsReadOnly calls it, and the
// startup heal of legacy overrides reads it.
func CalendarReadOnly(remoteAccess string) bool {
	return strings.EqualFold(strings.TrimSpace(remoteAccess), "read")
}

// CalendarSupportsComponent reports whether a calendars.remote_components
// value accepts the iCalendar component (for example "VEVENT"). An empty
// value or an empty component accepts all: such a row predates the
// discovery of the components. calendaraccess.EnsureWritable and the
// startup heal use the rule.
func CalendarSupportsComponent(remoteComponents, component string) bool {
	components := strings.TrimSpace(remoteComponents)
	if components == "" || strings.TrimSpace(component) == "" {
		return true
	}
	for _, advertised := range strings.Split(components, ",") {
		if strings.EqualFold(strings.TrimSpace(advertised), component) {
			return true
		}
	}
	return false
}

// CalendarLinked reports whether cal is linked to an account.
func CalendarLinked(cal Calendar) bool {
	return cal.AccountID != nil && *cal.AccountID != 0
}

// CalendarLockIDs maps each calendar of ids to its lock ID for
// synclock.Calendars. The lock ID is the account ID, or the negative
// calendar ID for a calendar without an account.
func CalendarLockIDs(ctx context.Context, q *Queries, ids []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(ids))
	for _, id := range ids {
		cal, err := q.GetCalendar(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get calendar %d for lifecycle lock: %w", id, err)
		}
		if CalendarLinked(cal) {
			out[id] = *cal.AccountID
		} else {
			out[id] = -id
		}
	}
	return out, nil
}

// MoveRelatedCalendars returns the calendars that a move links to
// calendarID, both ways:
//   - the targets of the live move conflicts whose source is calendarID
//   - the targets of the move tombstones of calendarID
//   - the sources of the move conflicts stored on calendarID
//   - the calendars whose tombstones name calendarID as the move target
//
// A reset or an unlink of calendarID writes or deletes those conflict rows
// (DetachMovesFromCalendar). It also changes how those tombstones wait, so
// the caller locks them too (issue #841).
func MoveRelatedCalendars(ctx context.Context, q *Queries, calendarID int64) ([]int64, error) {
	targets, err := q.ListMoveTargetsOfSource(ctx, &calendarID)
	if err != nil {
		return nil, fmt.Errorf("list the move targets of calendar %d: %w", calendarID, err)
	}
	sources, err := q.ListCalendarsOfTombstonesMovedTo(ctx, &calendarID)
	if err != nil {
		return nil, fmt.Errorf("list the move sources of calendar %d: %w", calendarID, err)
	}
	conflictSources, err := q.ListMoveSourcesOfTarget(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list the move conflict sources of calendar %d: %w", calendarID, err)
	}
	own, err := q.ListMoveTargetsOfOwnTombstones(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list the move tombstone targets of calendar %d: %w", calendarID, err)
	}
	out := append(targets, sources...)
	for _, id := range own {
		if id != nil {
			out = append(out, *id)
		}
	}
	for _, id := range conflictSources {
		if id != nil {
			out = append(out, *id)
		}
	}
	return out, nil
}

// MoveLockSet returns the resolve function of synclock.Calendars for
// calendarID and the calendars of MoveRelatedCalendars. A reset and an
// unlink of calendarID use it (issue #841).
func MoveLockSet(q *Queries, calendarID int64) func(context.Context) (map[int64]int64, error) {
	return func(ctx context.Context) (map[int64]int64, error) {
		related, err := MoveRelatedCalendars(ctx, q, calendarID)
		if err != nil {
			return nil, err
		}
		return CalendarLockIDs(ctx, q, append(related, calendarID))
	}
}

// DetachMovesFromCalendar ends the live move conflicts whose source is
// calendarID in the state detached, a failed cancel included. An unlink
// deletes the source tombstones, so those moves can never finish. Call it
// in the transaction of the unlink, under the locks of
// MoveRelatedCalendars (issue #841).
func DetachMovesFromCalendar(ctx context.Context, q *Queries, calendarID int64) error {
	if err := q.DetachMoveConflictsOfSource(ctx, &calendarID); err != nil {
		return fmt.Errorf("detach the moves from calendar %d: %w", calendarID, err)
	}
	return nil
}

// CalendarUnlinkedByAccountDelete reports whether cal lost its account to
// an account delete. The delete keeps remote_url, so a later add of the
// same origin can link the calendar again. A local calendar has no
// remote_url (issue #841).
func CalendarUnlinkedByAccountDelete(cal Calendar) bool {
	return !CalendarLinked(cal) && NullableToString(cal.RemoteUrl) != ""
}

// MoveResolveLockSet returns the resolve function of synclock.Calendars for
// the resolve of a move conflict of uid. The set holds the source, the
// target, and the calendars whose tombstones of uid name the target. The move back of a cancel rewrites
// those tombstones (recordResourceMove), so the resolve locks them too
// (issue #841).
func MoveResolveLockSet(q *Queries, sourceID, targetID int64, uid string) func(context.Context) (map[int64]int64, error) {
	return func(ctx context.Context) (map[int64]int64, error) {
		third, err := q.ListCalendarsOfTombstonesMovedToForUID(ctx, ListCalendarsOfTombstonesMovedToForUIDParams{TargetCalendarID: &targetID, Uid: uid})
		if err != nil {
			return nil, fmt.Errorf("list the tombstones that name calendar %d: %w", targetID, err)
		}
		return CalendarLockIDs(ctx, q, append([]int64{sourceID, targetID}, third...))
	}
}
