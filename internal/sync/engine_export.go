package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/event"
	hydratepkg "github.com/douglasdemoura/chroncal/internal/hydrate"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
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
	// softDeleteByUID soft-deletes the live rows of uid in one calendar.
	softDeleteByUID func(ctx context.Context, e *Engine, calendarID int64, uid string) error
	// lookupID finds the live master row of uid in one calendar.
	lookupID func(ctx context.Context, e *Engine, calendarID int64, uid string) (int64, error)
	// export bundles the series of uid in one calendar.
	export func(ctx context.Context, e *Engine, calendarID int64, uid string) ([]byte, error)
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
		softDeleteByUID: func(ctx context.Context, e *Engine, calendarID int64, uid string) error {
			// The server object held the series of the calendar, with the
			// legacy overrides that the series owns in other calendars.
			return e.q.SoftDeleteEventSeriesAndCalendarRows(ctx, storage.SoftDeleteEventSeriesAndCalendarRowsParams{CalendarID: calendarID, Uid: uid})
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
		softDeleteByUID: func(ctx context.Context, e *Engine, calendarID int64, uid string) error {
			return e.q.SoftDeleteTodosByCalendarUID(ctx, storage.SoftDeleteTodosByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
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
		softDeleteByUID: func(ctx context.Context, e *Engine, calendarID int64, uid string) error {
			return e.q.SoftDeleteJournalsByCalendarUID(ctx, storage.SoftDeleteJournalsByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
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
// export. With no master there, the override rows of the calendar are legacy
// overrides (see storage.LegacyOverride) when the UID has a live master in
// another calendar. They belong to the series of that master, and the export
// of its calendar carries them. The export of this calendar then has no
// resource (errResourceMissing). A PUT of the overrides alone would store an
// object with no master. A UID with no live master anywhere keeps the
// orphan-instance path: sql.ErrNoRows, and the override rows export on their
// own.
func (e *Engine) eventMasterForExport(ctx context.Context, calendarID int64, uid string) (event.Event, error) {
	// The common case: the live master of the calendar. One lookup on the
	// UNIQUE(calendar_id, uid, recurrence_id) key, with no view.
	own, err := e.q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: calendarID, Uid: uid})
	if err == nil {
		// The row with its categories. The hydrate step of the export
		// loads the relations and owns their failure mode.
		return e.events.Get(ctx, own.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return event.Event{}, fmt.Errorf("get master: %w", err)
	}
	// The view event_series_rows names the series that owns the rows of
	// the calendar, live or in the trash.
	owner, err := e.q.GetSeriesMasterOfCalendar(ctx, storage.GetSeriesMasterOfCalendarParams{
		CalendarID: calendarID, Uid: uid, IncludeDeleted: true,
	})
	found, err := storage.Found(err)
	if err != nil {
		return event.Event{}, fmt.Errorf("get series master: %w", err)
	}
	switch {
	case found && owner.DeletedAt == nil:
		// The live series of another calendar owns the rows here, and its
		// calendar exports them.
		return event.Event{}, fmt.Errorf("%w: event uid %s has its series in another calendar", errResourceMissing, uid)
	case found:
		// The master that owns the rows is in the trash, here or in
		// another calendar. Its series holds the rows, and its restore
		// sends them again. A PUT of the live rows alone would replace a
		// series object with a body with no master, or put the occurrence
		// on the server twice. The rows stay local until the master comes
		// back.
		return event.Event{}, fmt.Errorf("%w: event uid %s has its series master in the trash", errResourceMissing, uid)
	}
	// No owner. The rows here belong to no series: two or more masters
	// exist, live or in the trash, or the calendar holds no row. A PUT of
	// the rows would store a body with no master. Only a UID with no master
	// row anywhere leaves orphan instances to export.
	masters, err := e.q.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return event.Event{}, fmt.Errorf("list masters: %w", err)
	}
	if len(masters) > 0 {
		return event.Event{}, fmt.Errorf("%w: event uid %s has no owner here", errResourceMissing, uid)
	}
	return event.Event{}, sql.ErrNoRows
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

// deleteLocalResourceByUID soft-deletes the local rows of uid in
// calendarID after the server deleted the object of that calendar. The
// delete stays in that calendar: another calendar can hold the same UID (a
// local move, or a copy in another account).
func (e *Engine) deleteLocalResourceByUID(ctx context.Context, calendarID int64, ownerType, uid string) error {
	// Soft-delete across every owner type so a remote DELETE that races with
	// a user action doesn't nuke the local row — it stays in trash until the
	// retention window expires. The caller clears the sync_resource so a
	// later restore re-CREATEs a fresh one via MarkResourceDirty.
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return err
	}
	return ops.softDeleteByUID(ctx, e, calendarID, uid)
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
