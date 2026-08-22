package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/hydrate"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// WedgedResource describes a dirty sync resource whose export fails on a
// deterministic hydration error. Every sync retries it and fails the same
// way, and no edit under its UID reaches the server (issue #568).
type WedgedResource struct {
	CalendarID int64
	UID        string
	OwnerType  string
	// Relations names every relation whose load failed.
	Relations []string
	// PushFailCount counts the consecutive push attempts that failed
	// before this diagnosis. The push loop and this doctor increment it
	// after every failed attempt (issue #646).
	PushFailCount int64
}

// DiagnoseCalendar exports every dirty resource of one calendar and returns
// the ones that fail on unreadable relations. Transient export failures are
// not wedges; they stay out of the list. A resource enters the list only
// when its export names unreadable relations twice in a row, so a busy or
// cancelled database read never reads as a wedge.
func (e *Engine) DiagnoseCalendar(ctx context.Context, calendarID int64) ([]WedgedResource, error) {
	dirty, err := e.q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list dirty: %w", err)
	}
	var wedged []WedgedResource
	for _, res := range dirty {
		rels, ok := e.diagnoseWedge(ctx, res.OwnerType, res.Uid)
		if !ok {
			continue
		}
		wedged = append(wedged, WedgedResource{
			CalendarID:    calendarID,
			UID:           res.Uid,
			OwnerType:     res.OwnerType,
			Relations:     rels,
			PushFailCount: res.PushFailCount,
		})
	}
	return wedged, nil
}

// diagnoseWedge reports the unreadable relations when the export fails on
// them twice in a row. One failure is not proof of a wedge: SQLITE_BUSY
// after the busy timeout, an I/O error, or context cancellation all reach
// the collector like a corrupt row does. The retry separates a transient
// load failure from a deterministic one, so the destructive confirmation
// never fires on a false diagnosis.
func (e *Engine) diagnoseWedge(ctx context.Context, ownerType, uid string) ([]string, bool) {
	_, err := e.exportResource(ctx, ownerType, uid)
	if !wedgeShaped(err) {
		return nil, false
	}
	_, err = e.exportResource(ctx, ownerType, uid)
	if !wedgeShaped(err) {
		return nil, false
	}
	var hErr *hydrate.HydrationError
	errors.As(err, &hErr)
	rels := make([]string, 0, len(hErr.Failures))
	for _, f := range hErr.Failures {
		rels = append(rels, f.Relation)
	}
	return rels, true
}

// wedgeShaped reports whether err is a hydration failure that names broken
// relations and has no cancellation cause. A done context makes every load
// fail; those failures vanish on the next attempt and never describe a
// deterministic wedge.
func wedgeShaped(err error) bool {
	if err == nil {
		return false
	}
	var hErr *hydrate.HydrationError
	if !errors.As(err, &hErr) || len(hErr.Failures) == 0 {
		return false
	}
	for _, f := range hErr.Failures {
		if errors.Is(f.Cause, context.Canceled) || errors.Is(f.Cause, context.DeadlineExceeded) {
			return false
		}
	}
	return true
}

// DoctorPush pushes one wedged resource with best-effort hydration. The PUT
// omits every relation named in dropped, so the server copy loses exactly
// those fields. The caller must obtain explicit user confirmation first:
// this is the one path where chroncal knowingly pushes an incomplete record,
// and it exists so a deterministic local corruption cannot block every other
// edit forever (issue #568).
//
// The doctor deliberately skips the organizer gate (userOrganizesEvent) that
// the regular push enforces. The doctor exists to move resources the regular
// push cannot move. A foreign-organized meeting with an unreadable relation
// stays wedged without this bypass.
//
// diagnosed holds the relation set the user confirmed. The push re-runs the
// best-effort export under the lifecycle lock and refuses to send anything
// when the actual drop set differs: consent for one loss set must not
// silently cover another.
func (e *Engine) DoctorPush(ctx context.Context, calendarID int64, uid string, diagnosed []string) (dropped []string, err error) {
	// Hold the calendar lifecycle lock across the dirty snapshot, the PUT,
	// and the finalize, like SyncCalendar and PushLocalEdits do. A concurrent
	// sync run must not interleave between them (issue #647).
	release, err := e.lockCalendarLifecycle(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	defer release()

	cal, _, client, remoteURL, err := e.loadCalendarClient(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	if remoteCalendarIsReadOnly(cal) {
		return nil, fmt.Errorf("calendar %d is read-only", calendarID)
	}

	dirty, err := e.q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list dirty: %w", err)
	}
	var res *storage.SyncResource
	for i := range dirty {
		if dirty[i].Uid == uid {
			res = &dirty[i]
			break
		}
	}
	if res == nil {
		return nil, fmt.Errorf("no dirty sync resource for uid %q on calendar %d", uid, calendarID)
	}

	open, err := e.q.CountOpenSyncConflicts(ctx, storage.CountOpenSyncConflictsParams{
		CalendarID: calendarID,
		Uid:        res.Uid,
	})
	if err != nil {
		return nil, fmt.Errorf("count open sync conflicts %s: %w", uid, err)
	}
	if open > 0 {
		// The regular push skips conflicted resources because a PUT under an
		// open conflict desynchronizes the recorded conflict snapshots (issue
		// #104). The doctor must not converge the server around them either.
		return nil, fmt.Errorf("uid %s has an open sync conflict; resolve it first with chroncal sync resolve", uid)
	}

	icalData, dropped, err := e.exportResourceBestEffortFor(ctx, res.OwnerType, res.Uid)
	if err != nil {
		return nil, fmt.Errorf("best-effort export %s: %w", uid, err)
	}
	if !sameRelationSet(dropped, diagnosed) {
		return nil, fmt.Errorf(
			"the unreadable relation(s) changed since diagnosis: diagnosed [%s], now [%s]; re-run chroncal sync doctor",
			strings.Join(diagnosed, ", "), strings.Join(dropped, ", "))
	}

	body, parseErr := parseICalData(icalData)
	if parseErr != nil {
		return nil, fmt.Errorf("parse ical for %s: %w", uid, parseErr)
	}

	putPath := res.RemoteUrl
	if putPath == "" {
		putPath, err = client.CanonicalObjectRef(remoteURL, buildRemoteResourcePath(remoteURL, res.Uid))
		if err != nil {
			return nil, fmt.Errorf("build remote href for %s: %w", uid, err)
		}
	} else {
		putPath, err = client.CanonicalObjectRef(remoteURL, putPath)
		if err != nil {
			return nil, fmt.Errorf("validate remote href for %s: %w", uid, err)
		}
	}

	// A PUT can reach the server and still lose its response, which Retry
	// classifies as transient. The retried PUT re-sends the stale If-Match
	// and the server answers 412 forever. Mirror the regular push loop:
	// when an earlier attempt may have landed and the server now holds the
	// exact body we sent, adopt the landed ETag. See issues #294 and #647.
	priorAttemptMayHaveLanded := false
	newEtag, putErr := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
		etag, err := client.PutResource(ctx, putPath, body, res.Etag)
		if err == nil {
			return etag, nil
		}
		// A 412 is never transient, so these branches are exclusive.
		if caldav.IsTransient(err) {
			priorAttemptMayHaveLanded = true
		} else if priorAttemptMayHaveLanded && caldav.IsConflict(err) {
			if landedEtag, ok := e.putAlreadyLanded(ctx, client, putPath, body); ok {
				return landedEtag, nil
			}
		}
		return etag, err
	})

	if putErr != nil {
		putErr = fmt.Errorf("put %s: %w", uid, putErr)
		e.recordPushFailure(ctx, calendarID, uid, putErr)
		return dropped, putErr
	}

	if err := e.q.FinalizePushedResource(ctx, storage.FinalizePushedResourceParams{
		CalendarID: calendarID,
		Uid:        res.Uid,
		Etag:       newEtag,
		Rev:        res.Rev,
	}); err != nil {
		return dropped, fmt.Errorf("finalize pushed resource %s: %w", uid, err)
	}
	// The incomplete record reached the server. Reset the failure
	// bookkeeping like a regular successful push does.
	e.clearPushFailure(ctx, calendarID, uid)
	if res.RemoteUrl == "" {
		if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
			CalendarID:   calendarID,
			Uid:          res.Uid,
			OwnerType:    res.OwnerType,
			RemoteUrl:    normalizeRemoteRef(putPath),
			Etag:         newEtag,
			Dirty:        0,
			SyncStrategy: res.SyncStrategy,
		}); err != nil {
			return dropped, fmt.Errorf("update sync resource URL %s: %w", uid, err)
		}
	}
	e.logger.Warn("doctor push pushed an incomplete record",
		"uid", uid, "owner_type", res.OwnerType, "dropped_relations", dropped)
	return dropped, nil
}

// The per-type best-effort hydrate adapters return the dropped relation names
// in structured form. HydrateBestEffort returns a *hydrate.HydrationError, so
// the names come from the collector and no text parsing is involved.
func hydrateEventBestEffort(ctx context.Context, e *Engine, evt *event.Event) ([]string, error) {
	return droppedRelations(e.events.HydrateBestEffort(ctx, evt))
}

func hydrateTodoBestEffort(ctx context.Context, e *Engine, td *todo.Todo) ([]string, error) {
	return droppedRelations(e.todos.HydrateBestEffort(ctx, td))
}

func hydrateJournalBestEffort(ctx context.Context, e *Engine, j *journal.Journal) ([]string, error) {
	return droppedRelations(e.journals.HydrateBestEffort(ctx, j))
}

func droppedRelations(err error) ([]string, error) {
	if err == nil {
		return nil, nil
	}
	var hErr *hydrate.HydrationError
	if !errors.As(err, &hErr) {
		// Not a structured hydration report. Best-effort hydration has no
		// answer for it, so it stays fatal.
		return nil, err
	}
	// A HydrationError is the expected best-effort result: the record is
	// complete except for the named relations.
	rels := make([]string, 0, len(hErr.Failures))
	for _, f := range hErr.Failures {
		rels = append(rels, f.Relation)
	}
	return rels, nil
}

// exportResourceBestEffortFor dispatches on owner type. It runs the shared
// bundling path in exportResourceFor with the best-effort hydrators, so the
// doctor's payload assembly cannot drift from the regular push's.
func (e *Engine) exportResourceBestEffortFor(ctx context.Context, ownerType, uid string) ([]byte, []string, error) {
	switch ownerType {
	case ownerTypeEvent:
		return exportResourceFor(ctx, e, uid, ownerTypeEvent,
			e.events.GetByUID, e.events.ListOverridesByUID, hydrateEventBestEffort, icalPkg.ExportEvents)
	case ownerTypeTodo:
		return exportResourceFor(ctx, e, uid, ownerTypeTodo,
			e.todos.GetByUID, e.todos.ListOverridesByUID, hydrateTodoBestEffort, icalPkg.ExportTodos)
	case ownerTypeJournal:
		return exportResourceFor(ctx, e, uid, ownerTypeJournal,
			e.journals.GetByUID, e.journals.ListOverridesByUID, hydrateJournalBestEffort, icalPkg.ExportJournals)
	default:
		return nil, nil, fmt.Errorf("%w: %q", errUnknownOwnerType, ownerType)
	}
}

// sameRelationSet compares two relation sets order-insensitively.
func sameRelationSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := slices.Clone(a)
	sb := slices.Clone(b)
	slices.Sort(sa)
	slices.Sort(sb)
	return slices.Equal(sa, sb)
}
