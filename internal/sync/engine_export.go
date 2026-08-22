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

type tombstoneResult struct {
	deleted      int
	autoResolved int
	errors       []error
}

func (e *Engine) processTombstones(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*tombstoneResult, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}

	// Index the last-seen ETags so each DELETE can be made conditional. A real
	// lookup failure must abort rather than silently degrade to unconditional
	// DELETEs that could clobber a concurrent remote edit, so we surface it.
	syncResources, err := e.q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list sync resources: %w", err)
	}
	etagByUID := make(map[string]string, len(syncResources))
	for _, sr := range syncResources {
		etagByUID[sr.Uid] = sr.Etag
	}

	result := &tombstoneResult{}
	for _, ts := range tombstones {
		deletePath, hrefErr := client.CanonicalObjectRef(remoteURL, ts.RemoteUrl)
		if hrefErr != nil {
			result.errors = append(result.errors, fmt.Errorf("validate tombstone %s: %w", ts.Uid, hrefErr))
			continue
		}
		// Make the DELETE conditional on the ETag we last saw so the server
		// rejects it (412) if the resource was edited remotely after our last
		// sync. Without this, the tombstone push would silently destroy that
		// concurrent edit. An untracked resource (never synced, or already
		// cleaned up) has no ETag, which falls back to an unconditional DELETE.
		etag := etagByUID[ts.Uid]
		e.logger.Debug("deleting tombstone", "uid", ts.Uid, "remote_url", deletePath)
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.DeleteResource(ctx, deletePath, etag)
		}); err != nil && !errors.Is(err, caldav.ErrResourceGone) {
			// A 404/410 means the resource is already absent server-side —
			// the desired end state — so fall through and clear the local
			// rows instead of re-issuing the DELETE on every sync.
			if caldav.IsConflict(err) {
				// 412: the resource was edited remotely after we last saw it.
				// Honoring the delete would discard that edit, so abandon it.
				// Drop the tombstone (stop re-issuing the DELETE every sync)
				// but keep the sync_resource so the next pull re-imports the
				// remote version, resurrecting the item with its remote edit.
				// A delete-vs-edit conflict always preserves the remote edit
				// (the non-destructive choice), independent of ConflictStrategy.
				e.logger.Warn("tombstone delete conflict: remote edited, preserving remote version", "uid", ts.Uid)
				if err := e.q.DeleteTombstone(ctx, ts.ID); err != nil {
					e.logger.Warn("delete tombstone row after conflict failed", "uid", ts.Uid, "error", err)
				}
				result.autoResolved++
				continue
			}
			e.logger.Warn("delete remote resource failed", "uid", ts.Uid, "error", err)
			result.errors = append(result.errors, fmt.Errorf("delete tombstone %s: %w", ts.Uid, err))
			continue
		}
		if err := e.q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{
			CalendarID: calendarID,
			Uid:        ts.Uid,
		}); err != nil {
			e.logger.Warn("delete sync resource after tombstone", "uid", ts.Uid, "error", err)
		}
		if err := e.q.DeleteTombstone(ctx, ts.ID); err != nil {
			e.logger.Warn("delete tombstone row failed", "uid", ts.Uid, "error", err)
		}
		result.deleted++
	}
	return result, nil
}

func (e *Engine) syncCalendarMetadata(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) error {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return fmt.Errorf("get calendar for metadata sync: %w", err)
	}

	// Google CalendarList already supplies the color at discovery. Apple
	// calendar-color is not a Google CalDAV property, so never PROPFIND
	// it. A dirty local color still has to be written: CalendarList PATCH
	// is the Google equivalent of Apple calendar-color PROPPATCH. Clearing
	// ColorDirty after a successful write lets later Discover rounds adopt
	// CalendarList again. A failed write must not fail event sync
	// (issue #628); the dirty latch then keeps the local override.
	if caldav.IsGoogleCalendarEndpoint(remoteURL) {
		if cal.ColorDirty == 0 {
			return nil
		}
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.SetGoogleCalendarListColor(ctx, remoteURL, cal.Color)
		}); err != nil {
			e.logger.Warn("set google calendar color failed", "calendar_id", calendarID, "error", err)
			return nil
		}
		if err := e.calendars.ClearColorDirty(ctx, calendarID, cal.Color); err != nil {
			return fmt.Errorf("clear calendar color dirty: %w", err)
		}
		return nil
	}

	// A dirty local color wins: push it and clear the flag. Skip the remote
	// fetch entirely — its value would be discarded, and a failed fetch must
	// not block the pending push or strand ColorDirty (issue #419).
	if cal.ColorDirty != 0 {
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.SetCalendarColor(ctx, remoteURL, cal.Color)
		}); err != nil {
			return fmt.Errorf("set remote calendar color: %w", err)
		}
		if err := e.calendars.ClearColorDirty(ctx, calendarID, cal.Color); err != nil {
			return fmt.Errorf("clear calendar color dirty: %w", err)
		}
		return nil
	}

	remoteColor, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
		return client.GetCalendarColor(ctx, remoteURL)
	})
	if err != nil {
		// Color is decorative. A server that refuses calendar-color must
		// not fail the rest of the calendar sync (issue #628).
		e.logger.Warn("get remote calendar color failed", "calendar_id", calendarID, "error", err)
		return nil
	}
	if remoteColor == "" {
		// The server does not advertise a color. Keep the color that
		// discovery or the user already set.
		return nil
	}

	if remoteColor != storage.NullableToString(cal.RemoteColor) {
		if err := e.calendars.UpdateColorFromSync(ctx, calendarID, remoteColor, remoteColor); err != nil {
			return fmt.Errorf("update calendar color from sync: %w", err)
		}
	}

	return nil
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
