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
	softDeleteByUID func(ctx context.Context, e *Engine, uid string) error
	lookupID        func(ctx context.Context, e *Engine, uid string) (int64, error)
	export          func(ctx context.Context, e *Engine, uid string) ([]byte, error)
}

var ownerOpsByType = map[string]ownerOps{
	ownerTypeEvent: {
		softDeleteByUID: func(ctx context.Context, e *Engine, uid string) error {
			return e.q.SoftDeleteEventsByUID(ctx, uid)
		},
		lookupID: func(ctx context.Context, e *Engine, uid string) (int64, error) {
			row, err := e.q.GetEventByUID(ctx, uid)
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, uid string) ([]byte, error) {
			return exportResourceFor(ctx, e, uid, ownerTypeEvent,
				e.events.GetByUID, e.events.ListOverridesByUID, hydrateEvent, icalPkg.ExportEvents)
		},
	},
	ownerTypeTodo: {
		softDeleteByUID: func(ctx context.Context, e *Engine, uid string) error {
			return e.q.SoftDeleteTodosByUID(ctx, uid)
		},
		lookupID: func(ctx context.Context, e *Engine, uid string) (int64, error) {
			row, err := e.q.GetTodoByUID(ctx, uid)
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, uid string) ([]byte, error) {
			return exportResourceFor(ctx, e, uid, ownerTypeTodo,
				e.todos.GetByUID, e.todos.ListOverridesByUID, hydrateTodo, icalPkg.ExportTodos)
		},
	},
	ownerTypeJournal: {
		softDeleteByUID: func(ctx context.Context, e *Engine, uid string) error {
			return e.q.SoftDeleteJournalsByUID(ctx, uid)
		},
		lookupID: func(ctx context.Context, e *Engine, uid string) (int64, error) {
			row, err := e.q.GetJournalByUID(ctx, uid)
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, uid string) ([]byte, error) {
			return exportResourceFor(ctx, e, uid, ownerTypeJournal,
				e.journals.GetByUID, e.journals.ListOverridesByUID, hydrateJournal, icalPkg.ExportJournals)
		},
	},
}

// exportResourceFor bundles a UID's master row plus its override rows into an
// iCal payload. CalDAV tracks one resource per UID. Recurring resources are
// stored as a master row plus override rows that share the UID. We normally
// bundle master plus overrides so instance edits round-trip to the server.
//
// Google sometimes serves a single orphan instance under a UID like
// `<master>_R<recurrence-id>@google.com` with a RECURRENCE-ID property and no
// master in the same iCal stream. We import those as override rows with no
// master sibling. The master lookup then fails. We therefore append every
// live row under the UID and export the non-empty result. We return
// errResourceMissing only when nothing remains.
func exportResourceFor[T any](
	ctx context.Context,
	e *Engine,
	uid, kind string,
	get func(context.Context, string) (T, error),
	listOverrides func(context.Context, string) ([]T, error),
	hydrate func(context.Context, *Engine, *T) error,
	export func([]T, string) ([]byte, error),
) ([]byte, error) {
	var rows []T
	switch row, err := get(ctx, uid); {
	case err == nil:
		rows = append(rows, row)
	case !errors.Is(err, sql.ErrNoRows):
		// Only "no master row" is an expected outcome here (the orphan-instance
		// case described above). Treating a transient failure — SQLITE_BUSY, an
		// I/O error — the same way would export the overrides alone, PUT a
		// resource with the master VEVENT and its recurrence rule amputated,
		// then clear the dirty flag so nothing ever retries.
		return nil, fmt.Errorf("get %s master uid %s: %w", kind, uid, err)
	}
	overrides, err := listOverrides(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("list overrides for %s uid %s: %w", kind, uid, err)
	}
	rows = append(rows, overrides...)
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s uid %s", errResourceMissing, kind, uid)
	}
	for i := range rows {
		if err := hydrate(ctx, e, &rows[i]); err != nil {
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
				return nil, fmt.Errorf(
					"hydrate %s uid %s: unreadable relation(s) %s; the resource is stuck and no edit reaches the server until it is resolved (see chroncal sync doctor): %w",
					kind, uid, strings.Join(rels, ", "), err)
			}
			return nil, fmt.Errorf("hydrate %s uid %s: %w", kind, uid, err)
		}
	}
	return export(rows, "")
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

func (e *Engine) deleteLocalResourceByUID(ctx context.Context, ownerType, uid string) error {
	// Soft-delete across every owner type so a remote DELETE that races with
	// a user action doesn't nuke the local row — it stays in trash until the
	// retention window expires. The caller clears the sync_resource so a
	// later restore re-CREATEs a fresh one via MarkResourceDirty.
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return err
	}
	return ops.softDeleteByUID(ctx, e, uid)
}

// lookupOwnerID resolves the local row ID that backs a UID for the given owner
// type. It returns errUnknownOwnerType for an unrecognized type and the
// lookup error when no live row exists. Callers then never silently
// attribute a record to ID 0.
func (e *Engine) lookupOwnerID(ctx context.Context, ownerType, uid string) (int64, error) {
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return 0, err
	}
	return ops.lookupID(ctx, e, uid)
}

// errResourceMissing reports that no live local row exists for a UID we were
// asked to export. Push uses errors.Is on this to distinguish a local row
// that is gone (clear the dirty flag, stop the retry) from a real export
// failure.
var errResourceMissing = errors.New("local resource missing for uid")

// exportResource exports a local resource to iCal bytes. It dispatches on
// owner type. See exportResourceFor for the master/override bundle behavior.
func (e *Engine) exportResource(ctx context.Context, ownerType string, uid string) ([]byte, error) {
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return nil, err
	}
	return ops.export(ctx, e, uid)
}

// hydrateEvent/hydrateTodo/hydrateJournal adapt the domain services' Hydrate
// methods to the ownerOps export signature. The relation list itself lives in
// the services (event.Service.Hydrate and friends). Sync, file export, and
// the CLI then cannot drift apart on what a complete record contains.
func hydrateEvent(ctx context.Context, e *Engine, evt *event.Event) error {
	return e.events.Hydrate(ctx, evt)
}

func hydrateTodo(ctx context.Context, e *Engine, t *todo.Todo) error {
	return e.todos.Hydrate(ctx, t)
}

func hydrateJournal(ctx context.Context, e *Engine, j *journal.Journal) error {
	return e.journals.Hydrate(ctx, j)
}
