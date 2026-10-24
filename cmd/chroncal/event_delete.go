package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// eventResource adapts the event service to the shared verb builders.
var eventResource = resource{
	name: "event",
	resolve: func(ctx context.Context, a *app.App, ref, recurrenceID string) (row, error) {
		// event delete accepts --calendar, so its hint can name it.
		e, err := resolveEventFor(ctx, a, ref, recurrenceID, true)
		if err != nil {
			return row{}, err
		}
		return row{ID: e.ID, UID: e.UID, CalendarID: e.CalendarID, Summary: e.Title}, nil
	},
	del: func(ctx context.Context, a *app.App, target row) (int64, error) {
		meta, err := a.Events.DeleteWithUndo(ctx, target.ID)
		return meta.PushCalendar(target.CalendarID), err
	},
	resolveIn: func(ctx context.Context, a *app.App, ref, calendarName string, series bool) (row, error) {
		calendarID, err := resolveCalendarID(ctx, a, calendarName)
		if err != nil {
			return row{}, err
		}
		if series {
			if _, parseErr := strconv.ParseInt(ref, 10, 64); parseErr != nil {
				// A series delete of a UID in a calendar reads its target
				// with the rule of the service before the prompt. A delete
				// that the service would refuse asks nothing.
				target := row{UID: ref, CalendarID: calendarID}
				label, seriesCalendarID, err := a.Events.SeriesDeleteTarget(ctx, calendarID, ref)
				if err != nil {
					return row{}, seriesDeleteErr(ctx, a, target, err)
				}
				target.Summary, target.CalendarID = label, seriesCalendarID
				return target, nil
			}
		}
		resolve := resolveRowIn
		if series {
			resolve = resolveSeriesMaster
		}
		e, err := resolve(ctx, a, ref, calendarID, calendarName)
		if err != nil {
			return row{}, err
		}
		target := row{ID: e.ID, UID: e.UID, CalendarID: e.CalendarID, Summary: e.Title}
		if series {
			// The check accepted the row for --calendar, so the series is
			// the one of that calendar. A legacy override lives in another
			// calendar than its series.
			target.CalendarID = calendarID
		}
		return target, nil
	},
	resolveSeriesUID: func(ctx context.Context, a *app.App, uid string) (row, error) {
		// The service rule of a series delete with no calendar: the one
		// live master of the UID, or the orphan rows of one calendar.
		label, calendarID, err := a.Events.SeriesDeleteTarget(ctx, 0, uid)
		if err != nil {
			return row{}, seriesDeleteErr(ctx, a, row{UID: uid}, err)
		}
		return row{UID: uid, CalendarID: calendarID, Summary: label}, nil
	},
	delSeries: func(ctx context.Context, a *app.App, target row) ([]int64, error) {
		meta, err := a.Events.DeleteSeriesWithUndo(ctx, target.CalendarID, target.UID)
		if err != nil {
			return nil, seriesDeleteErr(ctx, a, target, err)
		}
		return meta.PushCalendarIDs(target.CalendarID), nil
	},
	restoreByID: func(ctx context.Context, a *app.App, id int64) (restorePush, error) {
		// The UID names the rows to compare. With no row, the restore
		// gives its own error.
		e, err := a.Events.GetIncludingDeleted(ctx, id)
		if err != nil {
			return restorePush{}, a.Events.RestoreByID(ctx, id)
		}
		return restoreEventRows(ctx, a, e.UID, func() error { return a.Events.RestoreByID(ctx, id) })
	},
	restoreByUID: func(ctx context.Context, a *app.App, uid string) (restorePush, error) {
		return restoreEventRows(ctx, a, uid, func() error { return a.Events.RestoreByUID(ctx, uid) })
	},
	purgeCandidate: func(ctx context.Context, a *app.App, id int64) (row, bool, error) {
		e, err := a.Events.GetIncludingDeleted(ctx, id)
		if err != nil {
			return row{}, false, err
		}
		return row{Summary: e.Title}, e.DeletedAt != nil, nil
	},
	purgeByID: func(ctx context.Context, a *app.App, id int64) error { return a.Events.PurgeByID(ctx, id) },
	purgeDeleted: func(ctx context.Context, a *app.App, cutoff time.Time) (int64, error) {
		n, err := a.Events.PurgeDeleted(ctx, cutoff)
		return int64(n), err
	},
	uidState: func(ctx context.Context, a *app.App, uid string) (bool, bool, error) {
		live, err := a.Queries.ListLiveEventCalendarIDsByUID(ctx, uid)
		if err != nil {
			return false, false, err
		}
		trashed, err := a.Queries.ListTrashedEventsByUID(ctx, uid)
		return len(live) > 0, len(trashed) > 0, err
	},
	errNotDeleted: event.ErrNotDeleted,
}

// requireOccurrenceAt verifies that the series behind master generates an
// instance exactly at at. A timestamp that matches nothing previously wrote a
// phantom EXDATE and exited 0 while the real occurrence survived (issue #745).
// The check runs on the raw rule set, so the original RECURRENCE-ID of a moved
// override stays deletable.
func requireOccurrenceAt(ctx context.Context, a *app.App, master event.Event, at time.Time) error {
	if recurrence.OccurrenceExistsAt(master, at) {
		return nil
	}
	// A live override at that RECURRENCE-ID is deletable even when an
	// imported EXDATE hides the master slot; the override row is its own
	// key. The override must belong to this series: an override of the
	// same UID in another calendar does not make the occurrence deletable
	// here (issue #842).
	if _, err := a.Events.SeriesOverride(ctx, master.CalendarID, master.UID, at.UTC().Format(time.RFC3339)); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look up override: %w", notFoundErrFor(err, "event", master.UID, true))
	}
	return noSuchOccurrenceErr(master, at)
}

// requireOccurrenceFrom verifies that truncating at at removes at least one
// instance, so --following cannot silently do nothing.
func requireOccurrenceFrom(ctx context.Context, a *app.App, master event.Event, at time.Time) error {
	if recurrence.HasOccurrenceFrom(master, at) {
		return nil
	}
	// Truncation also removes every live override at or after the cutoff,
	// so one of those makes the scope meaningful even when an imported
	// EXDATE hides every generated slot.
	hasOverride, oErr := a.Events.HasLiveOverrideFrom(ctx, master.CalendarID, master.UID, at)
	if oErr != nil && !errors.Is(oErr, sql.ErrNoRows) {
		return fmt.Errorf("check overrides: %w", oErr)
	}
	if hasOverride {
		return nil
	}
	return noSuchOccurrenceErr(master, at)
}

// masterForScope resolves a numeric-ID reference that hit an override row to
// its series master. The scope validators reason about the raw rule set;
// validating against an override's own (empty) rule set would let a moved
// display start pass and write a phantom EXDATE.
func masterForScope(ctx context.Context, a *app.App, resolved event.Event) (event.Event, error) {
	if resolved.RecurrenceID == "" {
		return resolved, nil
	}
	// The series is the one in the calendar of the override row: two
	// calendars can hold the same UID (issue #842).
	return a.Events.SeriesMaster(ctx, resolved.CalendarID, resolved.UID)
}

// resolveSeriesMaster resolves the row that event delete addresses. A numeric
// ref addresses one row by ID. A UID ref addresses the live master of the
// UID. Two calendars can hold the same UID (issue #842). calendarID names the
// calendar of the series. 0 accepts a UID with one live master only. A
// calendar that holds the series through a legacy override only names the
// master of that override (see event.Service.SeriesMaster). A numeric ref
// with a calendar must be a row of that calendar.
func resolveSeriesMaster(ctx context.Context, a *app.App, ref string, calendarID int64, wantCalendar string) (event.Event, error) {
	if _, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil || calendarID == 0 {
		e, err := resolveEventFor(ctx, a, ref, "", true)
		if err != nil {
			return event.Event{}, err
		}
		if calendarID != 0 {
			if err := checkRowInCalendar(ctx, a, e, calendarID, wantCalendar); err != nil {
				return event.Event{}, err
			}
		}
		return e, nil
	}
	e, err := a.Events.SeriesMaster(ctx, calendarID, ref)
	if err != nil {
		return event.Event{}, ambiguousSeriesErr(err, ref, calendarID, wantCalendar, true)
	}
	return e, nil
}

// checkRowInCalendar accepts a row of calendarID. It also accepts each legacy
// override in another calendar that the series of calendarID owns. This
// includes a row that loses the tie rule at its occurrence. Every other row
// is a contradiction of the row ID and --calendar.
func checkRowInCalendar(ctx context.Context, a *app.App, e event.Event, calendarID int64, wantCalendar string) error {
	if e.CalendarID == calendarID {
		return nil
	}
	if e.RecurrenceID != "" {
		owned, err := a.Events.IsSeriesRowOf(ctx, calendarID, e.ID)
		if err != nil {
			return fmt.Errorf("look up the series of event %d: %w", e.ID, err)
		}
		if owned {
			return nil
		}
	}
	return errInvalidInputf("event %d is in calendar %q, not in --calendar %q", e.ID, calendarName(ctx, a, e.CalendarID), wantCalendar)
}

// resolveRowIn resolves the row of a plain delete with --calendar. A
// numeric ref names one row (see checkRowInCalendar). A UID names the
// master of the UID in the calendar. With no master there, it names the
// one live row of the UID in the calendar. More rows give the row IDs to
// choose from. A plain delete never reaches a master in another
// calendar through a legacy override.
func resolveRowIn(ctx context.Context, a *app.App, ref string, calendarID int64, wantCalendar string) (event.Event, error) {
	if _, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
		e, err := resolveEventFor(ctx, a, ref, "", true)
		if err != nil {
			return event.Event{}, err
		}
		return e, checkRowInCalendar(ctx, a, e, calendarID, wantCalendar)
	}
	master, err := a.Events.GetByCalendarUID(ctx, calendarID, ref)
	if err == nil {
		return master, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return event.Event{}, err
	}
	e, err := a.Events.LiveRowInCalendar(ctx, calendarID, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return event.Event{}, errNotFoundf("event %s not found in calendar %q", ref, wantCalendar)
	}
	if err != nil {
		return event.Event{}, notFoundErrFor(err, "event", ref, true)
	}
	return e, nil
}

func noSuchOccurrenceErr(master event.Event, at time.Time) error {
	if recurrence.IsCancelledSeries(master) {
		return errInvalidInputf("series %q is cancelled; delete it whole with \"event delete %d --yes\"",
			safeText(master.Title), master.ID)
	}
	msg := fmt.Sprintf("no occurrence of %q matches %s", safeText(master.Title), at.Format(time.RFC3339))
	if next, ok := recurrence.NextOccurrenceAfter(master, at); ok {
		msg += fmt.Sprintf("; the next occurrence is %s", next.Format(time.RFC3339))
	}
	return errInvalidInputf("%s", msg)
}

// eventDeleteCmd composes the shared delete verb with the two event-only
// scopes. --recurrence-id excludes one occurrence (an EXDATE on the master
// plus an override soft-delete). --following truncates the series from a
// date. Both scopes act on the series master, so this wrapper resolves the
// master row and bypasses the shared single-row flow.
func eventDeleteCmd() *cobra.Command {
	cmd := newDeleteCmd(eventResource, verbHelp{
		short: "Delete an event",
		long: `Delete a single event, this occurrence, this and following
occurrences, or an entire recurring series.

--recurrence-id and --following take an RFC 3339 timestamp and address the
series by ID or UID. --recurrence-id excludes that one date (EXDATE, and
any override at that time). --following truncates the series at that date.`,
		example: `  chroncal event delete 42
  chroncal event delete standup-uid --recurrence-id 2026-04-07T12:00:00Z
  chroncal event delete standup-uid --following 2026-04-07T12:00:00Z
  chroncal event delete standup-uid --series
  chroncal event delete standup-uid --calendar Work --following 2026-04-07T12:00:00Z`,
	})

	var following, calendarName string
	cmd.Flags().StringVar(&following, "following", "", "delete this occurrence and every following one (RFC 3339 timestamp)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "calendar of the event, when the UID is in more than one calendar")
	cmd.Flags().Lookup("recurrence-id").Usage = "delete this occurrence (RFC 3339 timestamp)"

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		recurrenceID, err := cmd.Flags().GetString("recurrence-id")
		if err != nil {
			return err
		}
		series, err := cmd.Flags().GetBool("series")
		if err != nil {
			return err
		}

		if series && (recurrenceID != "" || following != "") {
			return errInvalidInputf("--series cannot be combined with --recurrence-id or --following")
		}
		if recurrenceID != "" && following != "" {
			return errInvalidInputf("--recurrence-id and --following are mutually exclusive")
		}
		if recurrenceID == "" && following == "" {
			// The shared delete verb resolves a bare UID through the
			// one-master rule. With --calendar, it resolves the reference
			// in that calendar (eventResource.resolveIn).
			return runDelete(cmd, eventResource, args, deleteOpts{Series: series, CalendarName: calendarName})
		}

		var followingAt, occurrenceAt time.Time
		if following != "" {
			followingAt, err = parseRFC3339Flag("following", following)
			if err != nil {
				return err
			}
		}
		if recurrenceID != "" {
			occurrenceAt, err = parseRFC3339Flag("recurrence-id", recurrenceID)
			if err != nil {
				return err
			}
		}

		a, err := initApp()
		if err != nil {
			return err
		}
		defer a.Close()
		ctx := context.Background()

		var calendarID int64
		if calendarName != "" {
			calendarID, err = resolveCalendarID(ctx, a, calendarName)
			if err != nil {
				return err
			}
		}

		// Occurrence scopes address the series through its master row. A reference
		// to a legacy override resolves to the master of its series. The delete
		// writes the master. The push after the delete must reach the calendar of
		// the master.
		e, err := resolveSeriesMaster(ctx, a, args[0], calendarID, calendarName)
		if err != nil {
			return fmt.Errorf("get event: %w", err)
		}
		e, err = masterForScope(ctx, a, e)
		if err != nil {
			return fmt.Errorf("get series master: %w", notFoundErrFor(err, "event", args[0], true))
		}

		if following != "" {
			if err := requireOccurrenceFrom(ctx, a, e, followingAt); err != nil {
				return err
			}
		} else if err := requireOccurrenceAt(ctx, a, e, occurrenceAt); err != nil {
			return err
		}

		question := fmt.Sprintf("Delete this occurrence of %q at %s?", safeText(e.Title), occurrenceAt.Format(time.RFC3339))
		if following != "" {
			question = fmt.Sprintf("Delete %q and following occurrences at %s?", safeText(e.Title), followingAt.Format(time.RFC3339))
		}
		if err := confirmDestructive(cmd, question); err != nil {
			return err
		}

		w := cmd.OutOrStdout()
		if following != "" {
			if err := a.Events.DeleteFromInstance(ctx, e.CalendarID, e.UID, followingAt); err != nil {
				return fmt.Errorf("delete this and following: %w", err)
			}
			return reportWritten(cmd, a, w, []int64{e.CalendarID},
				map[string]any{"deleted": true, "uid": e.UID, "following": followingAt.UTC().Format(time.RFC3339)},
				fmt.Sprintf("Deleted %q and following occurrences.\n", safeText(e.Title)))
		}

		if err := a.Events.DeleteInstance(ctx, e.CalendarID, e.UID, occurrenceAt); err != nil {
			return fmt.Errorf("delete occurrence: %w", notFoundErrFor(err, "event", e.UID, true))
		}
		return reportWritten(cmd, a, w, []int64{e.CalendarID},
			map[string]any{"deleted": true, "uid": e.UID, "recurrence_id": occurrenceAt.UTC().Format(time.RFC3339)},
			fmt.Sprintf("Deleted occurrence of %q at %s.\n", safeText(e.Title), occurrenceAt.Format(time.RFC3339)))
	}
	return cmd
}

// seriesDeleteErr maps the error of a series delete of target to the CLI
// error of that case.
func seriesDeleteErr(ctx context.Context, a *app.App, target row, err error) error {
	if target.CalendarID == 0 {
		// No calendar names the series yet: the message names the UID.
		switch {
		case errors.Is(err, event.ErrNotDeleted):
			return errNotFoundf("event series %s not found", target.UID)
		case errors.Is(err, event.ErrSeriesInTrash):
			return errInvalidInputf("the series %s is in the trash; restore the series first (chroncal event restore)", target.UID)
		}
		return notFoundErrFor(err, "event", target.UID, true)
	}
	if errors.Is(err, event.ErrNotDeleted) {
		return errNotFoundf("event series %s has no live row in calendar %q", target.UID, calendarName(ctx, a, target.CalendarID))
	}
	if errors.Is(err, event.ErrAmbiguousUID) {
		return ambiguousSeriesErr(err, target.UID, target.CalendarID, calendarName(ctx, a, target.CalendarID), true)
	}
	if errors.Is(err, event.ErrSeriesInTrash) {
		return errInvalidInputf("the series %s is in the trash in calendar %q; restore it first (chroncal event restore), or name the calendar of the live series with --calendar",
			target.UID, calendarName(ctx, a, target.CalendarID))
	}
	return err
}

// restoreEventRows runs restore, a restore of rows of uid, and lists the
// candidate calendars of its push. It reads the rows of the UID in the trash
// before and after the restore. The rows that leave the trash are the rows
// of the restore. A restore of a master can also bring back legacy
// overrides in other calendars. The candidates are the calendars of these
// rows, and the calendar of the series that owns the rows of each (see
// GetSeriesMasterOfCalendar). The calendar of another series of the UID
// stays out (issue #842), so its push sends no unrelated edits. A failed
// read after the restore goes to restorePush.err.
func restoreEventRows(ctx context.Context, a *app.App, uid string, restore func() error) (restorePush, error) {
	before, beforeErr := a.Queries.ListTrashedEventsByUID(ctx, uid)
	if err := restore(); err != nil {
		return restorePush{}, err
	}
	return eventRestorePush(ctx, a, uid, before, beforeErr), nil
}

// eventRestorePush lists the candidate calendars of an event restore (see
// restoreEventRows). before holds the rows of uid in the trash before the
// restore, and beforeErr is a failed read of them.
func eventRestorePush(ctx context.Context, a *app.App, uid string, before []storage.Event, beforeErr error) restorePush {
	p := restorePush{uid: uid, err: beforeErr}
	if beforeErr != nil {
		return p
	}
	after, err := a.Queries.ListTrashedEventsByUID(ctx, uid)
	if err != nil {
		p.err = err
		return p
	}
	stillTrashed := make(map[int64]bool, len(after))
	for _, r := range after {
		stillTrashed[r.ID] = true
	}
	for _, r := range before {
		if stillTrashed[r.ID] {
			continue
		}
		p.candidates = append(p.candidates, r.CalendarID)
		m, err := a.Queries.GetSeriesMasterOfCalendar(ctx, storage.GetSeriesMasterOfCalendarParams{CalendarID: r.CalendarID, Uid: uid, IncludeDeleted: true})
		if found, err := storage.Found(err); err != nil {
			p.err = err
			return p
		} else if found {
			p.candidates = append(p.candidates, m.CalendarID)
		}
	}
	return p
}
