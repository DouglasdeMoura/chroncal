package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/event"
	hydratepkg "github.com/douglasdemoura/chroncal/internal/hydrate"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/douglasdemoura/chroncal/internal/uidmove"
)

// Owner-type strings stamped on every sync_resource row and CalDAV change
// record. CalDAV tracks one resource per UID, partitioned by component type.
const (
	ownerTypeEvent   = "event"
	ownerTypeTodo    = "todo"
	ownerTypeJournal = "journal"
)

// errUnknownOwnerType reports a sync_resource OwnerType the engine does not
// recognize. Every owner-type dispatch surfaces it instead of a guess. A
// new (or misspelled) component type then fails loudly rather than a silent
// mis-resolve. Notably, lookupOwnerID no longer reports ID 0 without error.
var errUnknownOwnerType = errors.New("unknown owner type")

// ownerOps bundles the per-component-type operations the sync engine
// dispatches on a sync_resource's OwnerType. Each component type is enumerated
// exactly once in ownerOpsByType. A fourth type is then a single map
// entry rather than synchronized edits to parallel switches. A missed
// type cannot compile cleanly into a silent mis-dispatch.
type ownerOps struct {
	// softDeleteByUID soft-deletes the live rows of uid in one calendar. q
	// binds to the transaction of the caller.
	softDeleteByUID func(ctx context.Context, q *storage.Queries, calendarID int64, uid string) error
	// lookupID finds the live master row of uid in one calendar.
	lookupID func(ctx context.Context, e *Engine, calendarID int64, uid string) (int64, error)
	// export bundles the series of uid in one calendar.
	export func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]byte, error)
	// uidMove binds the move rules of uidmove to uid in the transaction of
	// qtx.
	uidMove func(qtx *storage.Queries, uid string) uidmove.Rows
	// countLiveRows counts the live rows of uid in one calendar, through q.
	// A caller in a transaction passes the queries of that transaction.
	countLiveRows func(ctx context.Context, q *storage.Queries, calendarID int64, uid string) (int64, error)
	// rowIDs lists the IDs of the rows of uid (live and in the trash) in
	// one calendar.
	rowIDs func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]int64, error)
	// calendarOfRow returns the calendar of one row. found is false for a
	// row that is gone.
	calendarOfRow func(ctx context.Context, e *Engine, id int64) (calendarID int64, found bool, err error)
	// countLiveRowsInMoveScope counts the live rows of uid outside one
	// calendar, in the calendars of its account and in the local
	// calendars.
	countLiveRowsInMoveScope func(ctx context.Context, e *Engine, calendarID int64, uid string) (int64, error)
}

// byUID adapts a lookup by UID to the calendar-scoped signature of
// exportResourceFor. The todo and journal tables keep one UID in one
// calendar, so their lookups take no calendar.
func byUID[T any](get func(context.Context, string) (T, error)) func(context.Context, int64, string) (T, error) {
	return func(ctx context.Context, _ int64, uid string) (T, error) {
		return get(ctx, uid)
	}
}

// overridesByUID is byUID for the override lookup, which also receives
// the outcome of the master lookup.
func overridesByUID[T any](list func(context.Context, string) ([]T, error)) func(context.Context, int64, string, bool) ([]T, error) {
	return func(ctx context.Context, _ int64, uid string, _ bool) ([]T, error) {
		return list(ctx, uid)
	}
}

var ownerOpsByType = map[string]ownerOps{
	ownerTypeEvent: {
		rowIDs: func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]int64, error) {
			return e.q.ListEventRowIDsOfUIDInCalendar(ctx, storage.ListEventRowIDsOfUIDInCalendarParams{CalendarID: calendarID, Uid: uid})
		},
		calendarOfRow: func(ctx context.Context, e *Engine, id int64) (int64, bool, error) {
			calendarID, err := e.q.GetEventCalendarOfRow(ctx, id)
			found, err := storage.Found(err)
			return calendarID, found, err
		},
		countLiveRowsInMoveScope: func(ctx context.Context, e *Engine, calendarID int64, uid string) (int64, error) {
			return e.q.CountLiveEventRowsInMoveScope(ctx, storage.CountLiveEventRowsInMoveScopeParams{CalendarID: calendarID, Uid: uid})
		},
		countLiveRows: func(ctx context.Context, q *storage.Queries, calendarID int64, uid string) (int64, error) {
			return q.CountLiveEventRowsInCalendar(ctx, storage.CountLiveEventRowsInCalendarParams{CalendarID: calendarID, Uid: uid})
		},
		uidMove: event.UIDMoveRows,
		softDeleteByUID: func(ctx context.Context, q *storage.Queries, calendarID int64, uid string) error {
			// A pull writes the rows of the pulled calendar only. A legacy
			// override in another calendar keeps its row (see AGENTS.md).
			// The delete of the pull takes a fresh delete token, so the undo
			// of an earlier delete of these rows refuses. The token and the
			// delete share the transaction of q.
			token, err := q.NextEventDeleteToken(ctx)
			if err != nil {
				return fmt.Errorf("delete token: %w", err)
			}
			return q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: calendarID, Uid: uid, DeleteToken: token})
		},
		lookupID: func(ctx context.Context, e *Engine, calendarID int64, uid string) (int64, error) {
			row, err := e.q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]byte, error) {
			data, _, err := exportResourceFor(ctx, e, calendarID, uid, ownerTypeEvent,
				e.eventMasterForExport, e.eventOverridesForExport, hydrateEvent, icalPkg.ExportEvents)
			return data, err
		},
	},
	ownerTypeTodo: {
		rowIDs: func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]int64, error) {
			return e.q.ListTodoRowIDsOfUIDInCalendar(ctx, storage.ListTodoRowIDsOfUIDInCalendarParams{CalendarID: calendarID, Uid: uid})
		},
		calendarOfRow: func(ctx context.Context, e *Engine, id int64) (int64, bool, error) {
			calendarID, err := e.q.GetTodoCalendarOfRow(ctx, id)
			found, err := storage.Found(err)
			return calendarID, found, err
		},
		countLiveRowsInMoveScope: func(ctx context.Context, e *Engine, calendarID int64, uid string) (int64, error) {
			return e.q.CountLiveTodoRowsInMoveScope(ctx, storage.CountLiveTodoRowsInMoveScopeParams{CalendarID: calendarID, Uid: uid})
		},
		countLiveRows: func(ctx context.Context, q *storage.Queries, calendarID int64, uid string) (int64, error) {
			return q.CountLiveTodoRowsInCalendar(ctx, storage.CountLiveTodoRowsInCalendarParams{CalendarID: calendarID, Uid: uid})
		},
		uidMove: todo.UIDMoveRows,
		softDeleteByUID: func(ctx context.Context, q *storage.Queries, calendarID int64, uid string) error {
			return q.SoftDeleteTodosByCalendarUID(ctx, storage.SoftDeleteTodosByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
		},
		lookupID: func(ctx context.Context, e *Engine, _ int64, uid string) (int64, error) {
			row, err := e.q.GetTodoByUID(ctx, uid)
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]byte, error) {
			data, _, err := exportResourceFor(ctx, e, calendarID, uid, ownerTypeTodo,
				byUID(e.todos.GetByUID), overridesByUID(e.todos.ListOverridesByUID), hydrateTodo, icalPkg.ExportTodos)
			return data, err
		},
	},
	ownerTypeJournal: {
		rowIDs: func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]int64, error) {
			return e.q.ListJournalRowIDsOfUIDInCalendar(ctx, storage.ListJournalRowIDsOfUIDInCalendarParams{CalendarID: calendarID, Uid: uid})
		},
		calendarOfRow: func(ctx context.Context, e *Engine, id int64) (int64, bool, error) {
			calendarID, err := e.q.GetJournalCalendarOfRow(ctx, id)
			found, err := storage.Found(err)
			return calendarID, found, err
		},
		countLiveRowsInMoveScope: func(ctx context.Context, e *Engine, calendarID int64, uid string) (int64, error) {
			return e.q.CountLiveJournalRowsInMoveScope(ctx, storage.CountLiveJournalRowsInMoveScopeParams{CalendarID: calendarID, Uid: uid})
		},
		countLiveRows: func(ctx context.Context, q *storage.Queries, calendarID int64, uid string) (int64, error) {
			return q.CountLiveJournalRowsInCalendar(ctx, storage.CountLiveJournalRowsInCalendarParams{CalendarID: calendarID, Uid: uid})
		},
		uidMove: journal.UIDMoveRows,
		softDeleteByUID: func(ctx context.Context, q *storage.Queries, calendarID int64, uid string) error {
			return q.SoftDeleteJournalsByCalendarUID(ctx, storage.SoftDeleteJournalsByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
		},
		lookupID: func(ctx context.Context, e *Engine, _ int64, uid string) (int64, error) {
			row, err := e.q.GetJournalByUID(ctx, uid)
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]byte, error) {
			data, _, err := exportResourceFor(ctx, e, calendarID, uid, ownerTypeJournal,
				byUID(e.journals.GetByUID), overridesByUID(e.journals.ListOverridesByUID), hydrateJournal, icalPkg.ExportJournals)
			return data, err
		},
	},
}

// eventMasterForExport finds the live master of uid in calendarID for the
// export. The event service holds the owner rule (event.Service.ExportMaster).
// A UID whose rows here belong to a series that this calendar does not
// export has no resource (errResourceMissing). A UID with no master row
// anywhere keeps the orphan-instance path: sql.ErrNoRows, and the override
// rows export on their own.
func (e *Engine) eventMasterForExport(ctx context.Context, calendarID int64, uid string) (event.Event, error) {
	m, err := e.events.ExportMaster(ctx, calendarID, uid)
	if errors.Is(err, event.ErrNotExportedHere) {
		return event.Event{}, fmt.Errorf("%w: event uid %s: %w", errResourceMissing, uid, err)
	}
	return m, err
}

// eventOverridesForExport lists the override rows of the export of uid in
// calendarID. masterFound reports the outcome of eventMasterForExport. With a
// master in the calendar, the series rule (ListSeriesOverrides) gives the
// overrides. With no master in the calendar, no master row exists anywhere.
// The rows of the UID in the calendar are then orphan instances, and the
// lookup takes them with no series rule.
func (e *Engine) eventOverridesForExport(ctx context.Context, calendarID int64, uid string, masterFound bool) ([]event.Event, error) {
	if !masterFound {
		return e.events.ListLiveOverridesInCalendar(ctx, calendarID, uid)
	}
	return e.events.ListSeriesOverrides(ctx, calendarID, uid)
}

// exportResourceFor bundles a UID's master row plus its override rows into an
// iCal payload. CalDAV tracks one resource per UID. Recurring resources are
// stored as a master row plus override rows that share the UID. We normally
// bundle master plus overrides so instance edits round-trip to the server.
//
// Two calendars can hold the same event UID (issue #842), so the bundle
// takes the master of calendarID and the overrides of that series only.
// The push of one calendar then never uploads the series of another.
//
// The hydrator decides the failure mode. The strict adapters return an error
// and nil dropped, so one unreadable relation aborts the export. The
// best-effort adapters (the sync doctor path) return the relation names they
// had to drop; the caller owns the decision to send the incomplete payload.
//
// Google sometimes serves a single orphan instance under a UID like
// `<master>_R<recurrence-id>@google.com`. The instance has a RECURRENCE-ID
// property and no master in the same iCal stream. The import stores such
// an instance as an override row with no master sibling. The master
// lookup then gives sql.ErrNoRows, and the export takes the override rows
// of the calendar (see eventOverridesForExport). The result holds the
// live rows of the bundle. The function returns errResourceMissing when
// the bundle is empty.
func exportResourceFor[T any](
	ctx context.Context,
	e *Engine,
	calendarID int64,
	uid, kind string,
	get func(context.Context, int64, string) (T, error),
	listOverrides func(ctx context.Context, calendarID int64, uid string, masterFound bool) ([]T, error),
	hydrate func(context.Context, *Engine, *T) ([]string, error),
	export func([]T, string) ([]byte, error),
) ([]byte, []string, error) {
	var rows []T
	masterFound := false
	switch row, err := get(ctx, calendarID, uid); {
	case err == nil:
		masterFound = true
		rows = append(rows, row)
	case !errors.Is(err, sql.ErrNoRows):
		// Only "no master row" is an expected outcome here (the orphan-instance
		// case described above). Treating a transient failure — SQLITE_BUSY, an
		// I/O error — the same way would export the overrides alone, PUT a
		// resource with the master VEVENT and its recurrence rule amputated,
		// then clear the dirty flag so nothing ever retries.
		return nil, nil, fmt.Errorf("get %s master uid %s: %w", kind, uid, err)
	}
	overrides, err := listOverrides(ctx, calendarID, uid, masterFound)
	if err != nil {
		return nil, nil, fmt.Errorf("list overrides for %s uid %s: %w", kind, uid, err)
	}
	rows = append(rows, overrides...)
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("%w: %s uid %s", errResourceMissing, kind, uid)
	}
	var dropped []string
	seenRel := make(map[string]struct{})
	for i := range rows {
		rels, err := hydrate(ctx, e, &rows[i])
		if err != nil {
			// A hydration failure means the exported iCal would silently omit
			// alarms/attendees/attachments/... and the PUT would strip them
			// from the server copy. Abort instead: the dirty flag stays set, so
			// the resource is retried whole on the next sync.
			//
			// A deterministic failure (a corrupt row a retry can never fix)
			// wedges the resource: every sync fails identically and no edit
			// under this UID reaches the server. Name the broken relations and
			// point at the escape hatch so the user learns what is stuck and
			// why (issue #568).
			var hErr *hydratepkg.HydrationError
			if errors.As(err, &hErr) && len(hErr.Failures) > 0 {
				rels := make([]string, 0, len(hErr.Failures))
				for _, f := range hErr.Failures {
					rels = append(rels, f.Relation)
				}
				return nil, nil, fmt.Errorf(
					"hydrate %s uid %s: unreadable relation(s) %s; the resource is stuck and no edit reaches the server until it is resolved (see chroncal sync doctor): %w",
					kind, uid, strings.Join(rels, ", "), err)
			}
			return nil, nil, fmt.Errorf("hydrate %s uid %s: %w", kind, uid, err)
		}
		for _, r := range rels {
			if _, ok := seenRel[r]; ok {
				continue
			}
			seenRel[r] = struct{}{}
			dropped = append(dropped, r)
		}
	}
	data, err := export(rows, "")
	return data, dropped, err
}

// ownerOpsFor resolves the dispatch table for an owner type. It returns
// errUnknownOwnerType for any type not registered above.
func ownerOpsFor(ownerType string) (ownerOps, error) {
	ops, ok := ownerOpsByType[ownerType]
	if !ok {
		return ownerOps{}, fmt.Errorf("%w: %q", errUnknownOwnerType, ownerType)
	}
	return ops, nil
}

// remoteDeleteOutcome is the result of deleteLocalResourceByUID.
type remoteDeleteOutcome int

const (
	// remoteDeleteKept: a tombstone holds the UID. The rows, the sync row,
	// and the tombstone stay.
	remoteDeleteKept remoteDeleteOutcome = iota
	// remoteDeleteDone: the local rows went to the trash.
	remoteDeleteDone
	// remoteDeleteRecreate: the sync row held a local edit that no push
	// sent. The rows stay, and the row loses its href and its etag, so the
	// next push creates the object again (issue #866).
	remoteDeleteRecreate
)

// deleteLocalResourceByUID applies a remote delete of uid in calendarID in
// one transaction. The delete stays in that calendar: another calendar can
// hold the same UID (a local move, or a copy in another account).
//
// The caller settles a tombstone of the UID before the call (see
// pendingDeletions.apply). A tombstone that a local delete or move writes
// after that read keeps every row. The tombstone pass then sends the
// DELETE, and its 404 settles the move with the move rules (issue #841).
//
// With keepEdit, a dirty sync row holds a local edit that no push sent: a
// restore from the trash, a move back into the calendar, or a plain edit.
// The local edit wins over the remote delete. The rows stay, and the row
// loses its href and its etag, so the next push creates the object again.
// A read-only calendar never pushes the edit, so the remote delete wins
// there (issue #866).
//
// Else the rows go to the trash, across every owner type, and the sync row
// goes. A later restore re-CREATEs a fresh sync row via MarkResourceDirty.
// The read of the tombstones and each write share the transaction.
func (e *Engine) deleteLocalResourceByUID(ctx context.Context, calendarID int64, ownerType, uid string, keepEdit bool) (remoteDeleteOutcome, error) {
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return remoteDeleteKept, err
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return remoteDeleteKept, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := e.q.WithTx(tx)
	_, err = qtx.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calendarID, Uid: uid})
	tombstoned, err := storage.Found(err)
	if err != nil {
		return remoteDeleteKept, fmt.Errorf("get tombstone: %w", err)
	}
	if tombstoned {
		return remoteDeleteKept, nil
	}
	if keepEdit {
		kept, err := keepUnpushedEdit(ctx, qtx, calendarID, uid)
		if err != nil {
			return remoteDeleteKept, err
		}
		if kept {
			return remoteDeleteRecreate, tx.Commit()
		}
	}
	if err := ops.softDeleteByUID(ctx, qtx, calendarID, uid); err != nil {
		return remoteDeleteKept, err
	}
	if err := qtx.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calendarID, Uid: uid}); err != nil {
		return remoteDeleteKept, fmt.Errorf("delete sync resource: %w", err)
	}
	return remoteDeleteDone, tx.Commit()
}

// keepUnpushedEdit drops the href and the etag of the sync row of uid when
// the row is dirty and the calendar can push. kept reports that the row
// stays for a create. See deleteLocalResourceByUID.
//
// An open conflict of the UID keeps the remote delete: the user did not
// pick a side yet. A kept row would wait for that pick without an href, and
// a server pick would then leave an item that no push sends.
func keepUnpushedEdit(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (kept bool, err error) {
	sr, err := qtx.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	found, err := storage.Found(err)
	if err != nil {
		return false, fmt.Errorf("get sync resource: %w", err)
	}
	if !found || sr.Dirty == 0 || sr.RemoteUrl == "" {
		return false, nil
	}
	open, err := qtx.CountOpenSyncConflicts(ctx, storage.CountOpenSyncConflictsParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		return false, fmt.Errorf("count the open conflicts: %w", err)
	}
	if open > 0 {
		return false, nil
	}
	cal, err := qtx.GetCalendar(ctx, calendarID)
	if err != nil {
		return false, fmt.Errorf("get calendar: %w", err)
	}
	if remoteCalendarIsReadOnly(cal) {
		return false, nil
	}
	n, err := qtx.ForgetGoneServerObject(ctx, storage.ForgetGoneServerObjectParams{CalendarID: calendarID, Uid: uid, RemoteUrl: sr.RemoteUrl})
	if err != nil {
		return false, fmt.Errorf("drop the href of the gone object: %w", err)
	}
	return n > 0, nil
}

// lookupOwnerID resolves the local row ID that backs a UID in calendarID
// for the given owner type. It returns errUnknownOwnerType for an
// unrecognized type and the lookup error when no live row exists. Callers
// then never silently attribute a record to ID 0.
func (e *Engine) lookupOwnerID(ctx context.Context, calendarID int64, ownerType, uid string) (int64, error) {
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return 0, err
	}
	return ops.lookupID(ctx, e, calendarID, uid)
}

// errResourceMissing reports that no live local row exists for a UID we were
// asked to export. Push uses errors.Is on this to distinguish a local row
// that is gone (clear the dirty flag, stop the retry) from a real export
// failure.
var errResourceMissing = errors.New("local resource missing for uid")

// exportResource exports the local resource of uid in calendarID to iCal
// bytes. It dispatches on owner type. See exportResourceFor for the
// master/override bundle behavior.
func (e *Engine) exportResource(ctx context.Context, calendarID int64, ownerType string, uid string) ([]byte, error) {
	if e.testHooks != nil && e.testHooks.exportFailure != nil {
		return nil, e.testHooks.exportFailure
	}
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return nil, err
	}
	return ops.export(ctx, e, calendarID, uid)
}

// hydrateEvent/hydrateTodo/hydrateJournal adapt the domain services' Hydrate
// methods to the ownerOps export signature. The relation list itself lives in
// the services (event.Service.Hydrate and friends). Sync, file export, and
// the CLI then cannot drift apart on what a complete record contains.
func hydrateEvent(ctx context.Context, e *Engine, evt *event.Event) ([]string, error) {
	return nil, e.events.Hydrate(ctx, evt)
}

func hydrateTodo(ctx context.Context, e *Engine, t *todo.Todo) ([]string, error) {
	return nil, e.todos.Hydrate(ctx, t)
}

func hydrateJournal(ctx context.Context, e *Engine, j *journal.Journal) ([]string, error) {
	return nil, e.journals.Hydrate(ctx, j)
}

// movedCopyExists reports whether calendarID still holds the moved copy of
// uid as a live copy. The view live_move_copies holds the rule, so the moot
// step (ListMootMoveConflicts) applies the same one. Live rows exist, no
// tombstone of the UID waits in the calendar, and a remote calendar also
// holds the sync row. A landed copy that the user deleted keeps its sync row
// and gets a plain tombstone, so the row alone proves nothing (issue #841).
func (e *Engine) movedCopyExists(ctx context.Context, calendarID int64, ownerType, uid string) (bool, error) {
	present, err := e.q.MovedCopyExists(ctx, storage.MovedCopyExistsParams{CalendarID: calendarID, Uid: uid, OwnerType: ownerType})
	if err != nil {
		return false, fmt.Errorf("check the moved copy: %w", err)
	}
	return present != 0, nil
}

// plannedHref returns the href that the first push of uid into calendarID
// uses. The name comes from the UID (buildRemoteResourcePath), and the
// canonical form comes from the server URL of the account. The href is then
// known before the push and without a credential, so an offline resolve
// works.
func (e *Engine) plannedHref(ctx context.Context, calendarID int64, uid string) (string, error) {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return "", fmt.Errorf("get calendar: %w", err)
	}
	if !storage.CalendarLinked(cal) {
		return "", fmt.Errorf("calendar %d is not linked to an account", calendarID)
	}
	account, err := e.q.GetAccount(ctx, *cal.AccountID)
	if err != nil {
		return "", fmt.Errorf("get account: %w", err)
	}
	remoteURL := storage.NullableToString(cal.RemoteUrl)
	if remoteURL == "" {
		return "", fmt.Errorf("calendar %d has no remote URL", calendarID)
	}
	return plannedObjectHref(account.ServerUrl, remoteURL, uid)
}

// plannedObjectHref builds the planned href of uid in the calendar
// remoteURL at the CalDAV endpoint. It reads nothing.
func plannedObjectHref(endpoint, remoteURL, uid string) (string, error) {
	return plannedHrefWith(func(calendarRef, objectRef string) (string, error) {
		return caldav.CanonicalObjectRefAt(endpoint, calendarRef, objectRef)
	}, remoteURL, uid)
}

// plannedHrefWith builds the planned href of uid in the calendar remoteURL
// with the canonicalizer canon, and normalizes it. plannedHref passes the
// endpoint of the account, and the push passes its client. So both give
// the same href.
func plannedHrefWith(canon func(calendarRef, objectRef string) (string, error), remoteURL, uid string) (string, error) {
	ref, err := canon(remoteURL, buildRemoteResourcePath(remoteURL, uid))
	if err != nil {
		return "", fmt.Errorf("build remote href for %s: %w", uid, err)
	}
	return normalizeRemoteRef(ref), nil
}
