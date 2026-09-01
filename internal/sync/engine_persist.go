package sync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// persistImported saves parsed iCal data to the local database using the same
// upsert pattern as the CLI import command.
//
// It returns the post-import sync_resources.rev for each persisted UID. The
// value is read inside the same transaction that bumped it via
// MarkResourceDirty. Accept-server callers feed that rev to
// FinalizePushedResource. The dirty clear is then guarded on a rev captured
// atomically with the import. It is not re-read after commit. A concurrent
// local edit could then slip in and have its dirty flag wiped. That is the
// lost-update window of issue #494. A UID with no sync_resources row yet (a
// first pull) reports rev 0.
//
// The second return value lists the alarms this function dropped. An alarm
// the write rule rejects must not fail its whole resource: the record and
// its valid alarms still persist, and the caller reports each drop as a
// warning (issue #585). Every other error still fails the resource, so the
// caller keeps it dirty and retries.
func (e *Engine) persistImported(ctx context.Context, calendarID int64, result icalPkg.ImportResult) (map[string]int64, []string, error) {
	revs := make(map[string]int64)
	var alarmWarnings []string

	// Build the prune inputs up front: per-UID keep-sets of the components
	// the server sent, plus each prunable UID's dirty flag — which must be
	// read before the upserts below flip it via MarkResourceDirty, because
	// the override pruning at the end needs the pre-import value to
	// distinguish "the server dropped this override" from "a local override
	// the server has never seen" (an unpushed local edit). A component the
	// parser dropped (SkippedComponents != 0) makes the keep-sets an
	// incomplete inventory, so pruning is disabled wholesale: the nil maps
	// below make pruneStaleOverrides a no-op.
	var eventKeep, todoKeep, journalKeep map[string]map[string]bool
	var dirtyBefore map[string]bool
	if result.SkippedComponents == 0 {
		eventKeep = keepSets(result.Events, func(v event.Event) (string, string) { return v.UID, v.RecurrenceID })
		todoKeep = keepSets(result.Todos, func(v todo.Todo) (string, string) { return v.UID, v.RecurrenceID })
		journalKeep = keepSets(result.Journals, func(v journal.Journal) (string, string) { return v.UID, v.RecurrenceID })
		var err error
		dirtyBefore, err = e.preImportDirty(ctx, calendarID, eventKeep, todoKeep, journalKeep)
		if err != nil {
			return nil, nil, err
		}
	}

	// Store timezones
	for _, tz := range result.Timezones {
		if _, err := e.q.UpsertTimezone(ctx, storage.UpsertTimezoneParams{
			Tzid:          tz.TZID,
			VtimezoneData: tz.Data,
		}); err != nil {
			e.logger.Warn("store VTIMEZONE", "tzid", tz.TZID, "error", err)
		}
	}

	// Import events. Each resource's upsert plus its child-collection replaces
	// run in one transaction (inTx) so a mid-sequence failure rolls back to the
	// prior consistent state rather than leaving a half-updated row (e.g. new
	// alarms but stale attendees). The resource stays dirty and is retried.
	for _, ev := range result.Events {
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			events := e.events.WithTx(tx)
			saved, err := events.UpsertByUID(ctx, event.UpsertParams{
				UID: ev.UID, CalendarID: calendarID,
				Title: ev.Title, Description: ev.Description, Location: ev.Location,
				StartTime: ev.StartTime, EndTime: ev.EndTime, AllDay: ev.AllDay,
				RecurrenceRule: ev.RecurrenceRule, Timezone: ev.Timezone,
				Status: ev.Status, Transp: ev.Transp, Sequence: ev.Sequence,
				Priority: ev.Priority, Class: ev.Class, URL: ev.URL,
				ConferenceURI: ev.ConferenceURI,
				Categories:    ev.Categories, ExDates: ev.ExDates, RDates: ev.RDates,
				RecurrenceID: ev.RecurrenceID, Geo: ev.Geo,
				DurationValue: ev.DurationValue, DtStamp: ev.DtStamp,
			})
			if err != nil {
				return fmt.Errorf("upsert event %q: %w", ev.UID, err)
			}
			// Replace child collections unconditionally so server-side removals
			// (an empty list) are propagated, mirroring how Categories are handled
			// via UpsertByUID. A full CalDAV pull sends the complete component, so
			// the absence of a property means "cleared", not "unknown". Propagate
			// any replace error so the caller keeps the resource dirty and retries.
			// An alarm the write rule rejects is the one exception. The partition
			// drops it, and the record still persists (issue #585).
			okAlarms, badAlarms := model.PartitionAlarmsForWrite(ev.Alarms)
			for _, bad := range badAlarms {
				alarmWarnings = append(alarmWarnings,
					fmt.Sprintf("event %q: alarm %d dropped: %v", ev.UID, bad.Index, bad.Err))
			}
			if err := events.ReplaceAlarmsForSync(ctx, saved.ID, okAlarms); err != nil {
				return fmt.Errorf("replace alarms for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceAttendeesForSync(ctx, saved.ID, ev.Attendees); err != nil {
				return fmt.Errorf("replace attendees for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceAttachments(ctx, saved.ID, ev.Attachments); err != nil {
				return fmt.Errorf("replace attachments for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceComments(ctx, saved.ID, ev.Comments); err != nil {
				return fmt.Errorf("replace comments for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceContacts(ctx, saved.ID, ev.Contacts); err != nil {
				return fmt.Errorf("replace contacts for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceResources(ctx, saved.ID, ev.Resources); err != nil {
				return fmt.Errorf("replace resources for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceRelations(ctx, saved.ID, ev.Relations); err != nil {
				return fmt.Errorf("replace relations for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceXProperties(ctx, saved.ID, ev.XProperties); err != nil {
				return fmt.Errorf("replace xproperties for event %q: %w", ev.UID, err)
			}
			rev, err := captureImportRev(ctx, e.q.WithTx(tx), calendarID, ev.UID)
			if err != nil {
				return fmt.Errorf("capture rev for event %q: %w", ev.UID, err)
			}
			revs[ev.UID] = rev
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	// Import todos. One transaction per resource; see the event loop above.
	for _, t := range result.Todos {
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			todos := e.todos.WithTx(tx)
			saved, err := todos.UpsertByUID(ctx, todo.UpsertParams{
				UID: t.UID, CalendarID: calendarID,
				Summary: t.Summary, Description: t.Description, Location: t.Location,
				DueDate: t.DueDate, StartDate: t.StartDate, Duration: t.Duration,
				CompletedAt: t.CompletedAt, PercentComplete: t.PercentComplete,
				Status: t.Status, Priority: t.Priority, Class: t.Class,
				URL: t.URL, Categories: t.Categories,
				RecurrenceRule: t.RecurrenceRule, Timezone: t.Timezone,
				Sequence: t.Sequence, ExDates: t.ExDates, RDates: t.RDates,
				RecurrenceID: t.RecurrenceID, Geo: t.Geo,
				DtStamp: t.DtStamp,
			})
			if err != nil {
				return fmt.Errorf("upsert todo %q: %w", t.UID, err)
			}
			// Replace child collections unconditionally so server-side removals
			// (an empty list) are propagated. See the event loop above.
			okAlarms, badAlarms := model.PartitionAlarmsForWrite(t.Alarms)
			for _, bad := range badAlarms {
				alarmWarnings = append(alarmWarnings,
					fmt.Sprintf("todo %q: alarm %d dropped: %v", t.UID, bad.Index, bad.Err))
			}
			if err := todos.ReplaceAlarmsForSync(ctx, saved.ID, okAlarms); err != nil {
				return fmt.Errorf("replace alarms for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceAttendees(ctx, saved.ID, t.Attendees); err != nil {
				return fmt.Errorf("replace attendees for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceAttachments(ctx, saved.ID, t.Attachments); err != nil {
				return fmt.Errorf("replace attachments for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceComments(ctx, saved.ID, t.Comments); err != nil {
				return fmt.Errorf("replace comments for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceContacts(ctx, saved.ID, t.Contacts); err != nil {
				return fmt.Errorf("replace contacts for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceResources(ctx, saved.ID, t.Resources); err != nil {
				return fmt.Errorf("replace resources for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceRelations(ctx, saved.ID, t.Relations); err != nil {
				return fmt.Errorf("replace relations for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceXProperties(ctx, saved.ID, t.XProperties); err != nil {
				return fmt.Errorf("replace xproperties for todo %q: %w", t.UID, err)
			}
			rev, err := captureImportRev(ctx, e.q.WithTx(tx), calendarID, t.UID)
			if err != nil {
				return fmt.Errorf("capture rev for todo %q: %w", t.UID, err)
			}
			revs[t.UID] = rev
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	// Import journals. One transaction per resource; see the event loop above.
	for _, j := range result.Journals {
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			journals := e.journals.WithTx(tx)
			saved, err := journals.UpsertByUID(ctx, journal.UpsertParams{
				UID: j.UID, CalendarID: calendarID,
				Summary: j.Summary, Description: j.Description,
				StartDate: j.StartDate, Status: j.Status, Class: j.Class,
				URL: j.URL, Categories: j.Categories,
				RecurrenceRule: j.RecurrenceRule, Timezone: j.Timezone,
				Sequence: j.Sequence, ExDates: j.ExDates, RDates: j.RDates,
				RecurrenceID: j.RecurrenceID,
				DtStamp:      j.DtStamp,
			})
			if err != nil {
				return fmt.Errorf("upsert journal %q: %w", j.UID, err)
			}
			// Replace child collections unconditionally so server-side removals
			// (an empty list) are propagated. See the event loop above.
			if err := journals.ReplaceAttendees(ctx, saved.ID, j.Attendees); err != nil {
				return fmt.Errorf("replace attendees for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceAttachments(ctx, saved.ID, j.Attachments); err != nil {
				return fmt.Errorf("replace attachments for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceComments(ctx, saved.ID, j.Comments); err != nil {
				return fmt.Errorf("replace comments for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceContacts(ctx, saved.ID, j.Contacts); err != nil {
				return fmt.Errorf("replace contacts for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceRelations(ctx, saved.ID, j.Relations); err != nil {
				return fmt.Errorf("replace relations for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceXProperties(ctx, saved.ID, j.XProperties); err != nil {
				return fmt.Errorf("replace xproperties for journal %q: %w", j.UID, err)
			}
			rev, err := captureImportRev(ctx, e.q.WithTx(tx), calendarID, j.UID)
			if err != nil {
				return fmt.Errorf("capture rev for journal %q: %w", j.UID, err)
			}
			revs[j.UID] = rev
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	// Prune overrides the server dropped (e.g. a deleted instance that became
	// an EXDATE on the master); see pruneStaleOverrides for the safety gates.
	if err := pruneStaleOverrides(ctx, e, calendarID, eventKeep, dirtyBefore, revs,
		func(ctx context.Context, uid string) ([]storage.Event, error) {
			rows, err := e.q.ListOverridesByUID(ctx, uid)
			if err != nil {
				return nil, err
			}
			out := make([]storage.Event, 0, len(rows))
			for _, r := range rows {
				if r.CalendarID == calendarID {
					out = append(out, r)
				}
			}
			return out, nil
		},
		func(v storage.Event) int64 { return v.ID },
		func(v storage.Event) string { return v.RecurrenceID },
		(*storage.Queries).SoftDeleteEvent,
	); err != nil {
		return nil, nil, fmt.Errorf("prune stale event overrides: %w", err)
	}
	if err := pruneStaleOverrides(ctx, e, calendarID, todoKeep, dirtyBefore, revs,
		e.q.ListTodoOverridesByUID,
		func(v storage.Todo) int64 { return v.ID },
		func(v storage.Todo) string { return v.RecurrenceID },
		(*storage.Queries).SoftDeleteTodo,
	); err != nil {
		return nil, nil, fmt.Errorf("prune stale todo overrides: %w", err)
	}
	if err := pruneStaleOverrides(ctx, e, calendarID, journalKeep, dirtyBefore, revs,
		e.q.ListJournalOverridesByUID,
		func(v storage.Journal) int64 { return v.ID },
		func(v storage.Journal) string { return v.RecurrenceID },
		(*storage.Queries).SoftDeleteJournal,
	); err != nil {
		return nil, nil, fmt.Errorf("prune stale journal overrides: %w", err)
	}

	return revs, alarmWarnings, nil
}

// keepSets groups imported components into per-UID keep-sets of their
// RECURRENCE-IDs, the inventory pruneStaleOverrides reconciles against.
// Returns nil for an empty slice so empty domains cost nothing.
func keepSets[C any](items []C, key func(C) (uid, rid string)) map[string]map[string]bool {
	if len(items) == 0 {
		return nil
	}
	keeps := make(map[string]map[string]bool)
	for _, item := range items {
		uid, rid := key(item)
		keep := keeps[uid]
		if keep == nil {
			keep = make(map[string]bool)
			keeps[uid] = keep
		}
		keep[rid] = true
	}
	return keeps
}

// preImportDirty reads the sync dirty flag for every UID that override
// prune may reconcile. Those are UIDs whose master (recurrence_id == "") is
// in their keep-set. It must run before persistImported's upserts, which flip
// the flag via MarkResourceDirty. A UID with no sync_resources row (a first
// pull) reads as clean.
func (e *Engine) preImportDirty(ctx context.Context, calendarID int64, keeps ...map[string]map[string]bool) (map[string]bool, error) {
	dirty := make(map[string]bool)
	for _, keepByUID := range keeps {
		for uid, keep := range keepByUID {
			if !keep[""] {
				continue
			}
			if _, seen := dirty[uid]; seen {
				continue
			}
			res, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{
				CalendarID: calendarID,
				Uid:        uid,
			})
			if errors.Is(err, sql.ErrNoRows) {
				dirty[uid] = false
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read pre-import sync state for %q: %w", uid, err)
			}
			dirty[uid] = res.Dirty != 0
		}
	}
	return dirty, nil
}

// pruneStaleOverrides soft-deletes local override rows whose recurrence_id the
// server no longer has. When a CalDAV server deletes a recurring instance it
// drops the override component from the resource and adds the slot to the
// master's EXDATE. The master upsert carries the EXDATE. The stale
// override row must be pruned separately. Otherwise expansion restores the
// deleted instance. The orphan checker ignores EXDATEs so a legitimate
// override is never mistaken for an orphan.
//
// This is the sanctioned row-granularity counterpart of pendingDeletions'
// absence-inferred deletions (see that type's comment). It obeys the same
// rule: absence only counts against a provably complete inventory. The caller
// passes nil keep-sets when the parser dropped a component (an incomplete
// inventory). Each UID is reconciled only when:
//   - its own master (recurrence_id == "") is in its keep-set. An
//     overrides-only resource is unusual. Another UID's master says
//     nothing about this UID's inventory.
//   - its resource was not dirty before this import. A dirty resource has
//     unpushed local changes. A locally created override is absent from
//     the server body because the server did not see it, not because it
//     was deleted.
//   - its rev is unchanged inside the delete transaction. A local edit that
//     landed after this import bumped rev. The rows listed here may no
//     longer reflect it.
//
// A skipped prune is safe. The rows stay live. The dirty records that
// blocked it push or reconcile them on a later cycle.
func pruneStaleOverrides[R any](
	ctx context.Context,
	e *Engine,
	calendarID int64,
	keepByUID map[string]map[string]bool,
	dirtyBefore map[string]bool,
	revs map[string]int64,
	list func(context.Context, string) ([]R, error),
	idOf func(R) int64,
	ridOf func(R) string,
	del func(*storage.Queries, context.Context, int64) error,
) error {
	for uid, keep := range keepByUID {
		if !keep[""] || dirtyBefore[uid] {
			continue
		}
		existing, err := list(ctx, uid)
		if err != nil {
			return fmt.Errorf("list overrides %q: %w", uid, err)
		}
		var stale []int64
		for _, o := range existing {
			if !keep[ridOf(o)] {
				stale = append(stale, idOf(o))
			}
		}
		// The common case — a resource with no stale overrides — ends here,
		// without paying for a transaction.
		if len(stale) == 0 {
			continue
		}
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			qtx := e.q.WithTx(tx)
			rev, err := captureImportRev(ctx, qtx, calendarID, uid)
			if err != nil {
				return err
			}
			if rev != revs[uid] {
				return nil
			}
			for _, id := range stale {
				if err := del(qtx, ctx, id); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("prune overrides %q: %w", uid, err)
		}
	}
	return nil
}

// captureImportRev reads the sync_resources.rev for uid using qtx (a Queries
// bound to the import's transaction). The value then reflects the rev as
// bumped by this import and nothing committed after it. A UID with no
// sync_resources row yet (a first pull, before UpsertSyncResource creates it)
// reports rev 0. See #494.
func captureImportRev(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (int64, error) {
	res, err := qtx.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        uid,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return res.Rev, nil
}

// inTx runs fn inside a single transaction. It commits on success and rolls
// back on any error. It is the atomicity boundary for persistImported. A
// failed Replace* part-way through a resource unwinds the whole resource.
// The local row then never reflects a partial server component.
func (e *Engine) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// importICal parses raw iCal data and persists it to the local database via
// persistImported. It is the shared accept-the-server-version path used both by
// auto-resolve (ConflictServerWins) and manual conflict resolution. The local
// row then reflects the server data instead of the divergent local copy.
//
// imported reports whether the payload carried at least one VEVENT, VTODO,
// or VJOURNAL component. ImportFile returns no error for empty or
// component-less input. Callers that must not accept the server version
// without an apply (for example a clear of the dirty flag) check imported.
// They then do not stamp the server ETag onto an unchanged local row in
// silence.
func (e *Engine) importICal(ctx context.Context, calendarID int64, data string) (imported bool, revs map[string]int64, warnings []ImportWarning, err error) {
	importResult, err := icalPkg.ImportFileRemote(strings.NewReader(data))
	if err != nil {
		return false, nil, nil, fmt.Errorf("import ical: %w", err)
	}
	// Collect warnings before the tombstone filter below: dropping a
	// component must not change which component a warning is attributed to.
	warnings = e.noteImportWarnings("", importResult)
	// imported reflects whether the SERVER payload carried any component. It is
	// computed before tombstone filtering so the empty-iCal guard in callers
	// still fires for a genuinely empty server version, and never falsely fires
	// just because the only component was tombstoned away.
	imported = len(importResult.Events) > 0 || len(importResult.Todos) > 0 || len(importResult.Journals) > 0

	// Tombstone-aware import: drop any UID the user has locally deleted
	// (tombstoned, pending propagation to the server). UpsertByUID clears
	// deleted_at, so persisting a tombstoned UID would resurrect a row the user
	// just deleted. The pull path filters tombstoned UIDs inline (see the NOTE
	// in db/queries/events.sql); doing the same here keeps the accept-server
	// conflict paths — manual `sync resolve <id> server` and auto
	// ConflictServerWins — consistent with it. Issue #89 gap #2.
	importResult, err = e.dropTombstonedFromImport(ctx, calendarID, importResult)
	if err != nil {
		return false, nil, nil, err
	}
	revs, alarmWarnings, err := e.persistImported(ctx, calendarID, importResult)
	if err != nil {
		return false, nil, nil, err
	}
	warnings = append(warnings, e.notePersistWarnings("", "", alarmWarnings)...)
	if e.testHooks != nil && e.testHooks.afterImportPersist != nil {
		e.testHooks.afterImportPersist()
	}
	return imported, revs, warnings, nil
}

// fetchedResource is one remote resource body queued for import during a
// pull: the canonical remote path (the bookkeeping key for
// UpsertSyncResource), the href exactly as the server reported it (the log
// label), the etag, and the parsed iCal body.
type fetchedResource struct {
	path string
	href string
	etag string
	data *ical.Calendar
}

// importFetchedResource imports one fetched remote body and persists it with
// sync bookkeeping: encode → ImportFileRemote → note warnings → extract UID
// → tombstone check → conflict refresh → persist → UpsertSyncResource →
// clear-dirty. applySyncCollection and pullFullSnapshot share it, so a
// safety gate added for one pull path holds for the other too.
//
// uid is the UID the body carried. It is set even for a body the gates below
// skipped, because both pull loops count a fetched UID as still present on
// the server before they decide not to import it. imported reports whether
// the body produced a live import. warnings carries the import and persist
// warnings for the pull result. err is non-nil only when the import reached
// persistImported and the persist failed; the caller must then treat the
// pull as incomplete and withhold the sync-token.
func (e *Engine) importFetchedResource(ctx context.Context, calendarID int64, tombstonedUIDs map[string]bool, res fetchedResource) (uid string, imported bool, warnings []ImportWarning, err error) {
	var buf bytes.Buffer
	enc := ical.NewEncoder(&buf)
	if encErr := enc.Encode(res.data); encErr != nil {
		e.logger.Warn("encode fetched resource failed", "path", res.href, "error", encErr)
		return "", false, nil, nil
	}
	importResult, impErr := icalPkg.ImportFileRemote(strings.NewReader(buf.String()))
	if impErr != nil {
		e.logger.Warn("import fetched resource failed", "path", res.href, "error", impErr)
		return "", false, nil, nil
	}
	warnings = append(warnings, e.noteImportWarnings(res.href, importResult)...)

	// Extract UID from imported data
	uid = extractUID(importResult)
	if uid == "" {
		e.logger.Warn("no UID in fetched resource", "path", res.href)
		return "", false, warnings, nil
	}
	if tombstonedUIDs[uid] {
		e.logger.Debug("skip tombstoned remote resource by uid", "uid", uid, "path", res.path)
		return uid, false, warnings, nil
	}
	if e.hasOpenConflict(ctx, calendarID, uid) {
		e.logger.Debug("skip pull: open conflict pending resolution", "uid", uid)
		// The fetched body is newer than the recorded one. Record it so a
		// later resolve picks current server data. The sync-token may then
		// advance: the row, not the token, carries the obligation.
		e.refreshConflictServerBody(ctx, calendarID, uid, buf.String(), res.etag)
		return uid, false, warnings, nil
	}

	// Persist imported data to the database
	revs, alarmWarnings, persistErr := e.persistImported(ctx, calendarID, importResult)
	if persistErr != nil {
		// A changed body we fetched but couldn't store (transient SQLite
		// busy/lock, or a malformed component a Replace* rejects). Leave the
		// sync_resource on its old etag and report the failure so the caller
		// treats the pull as incomplete and withholds the sync-token. The
		// next pull then re-lists or re-fetches this change for another
		// attempt. Advancing past it would skip the change permanently
		// until the server touches it again.
		e.logger.Error("persist imported resource", "uid", uid, "path", res.href, "error", persistErr)
		return uid, false, warnings, persistErr
	}
	warnings = append(warnings, e.notePersistWarnings(res.href, uid, alarmWarnings)...)

	// Upsert sync resource tracking. UpsertSyncResource's ON CONFLICT is
	// keyed by (calendar_id, uid), so a stale remote_url from a prior sync
	// cycle (or from our PUT before the server rewrote the href) gets
	// replaced here with the authoritative server path.
	ownerType := detectOwnerType(importResult)
	if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          uid,
		OwnerType:    ownerType,
		RemoteUrl:    res.path,
		Etag:         res.etag,
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		e.logger.Error("upsert sync resource", "uid", uid, "error", err)
	}
	// persistImported goes through the event/todo/journal services, whose
	// Replace* methods all flip dirty=1 via MarkResourceDirty as a side
	// effect (correct for user-initiated edits, wrong for sync-driven
	// imports). UpsertSyncResource's `dirty = MAX(...)` clause then preserves
	// that 1, so without an explicit clear here every pull re-dirties
	// everything it just absorbed and the next push round-trips it back to
	// the server. Clear dirty since the server's version is now
	// authoritative locally, but guard the clear on the rev persistImported
	// captured inside its transaction so a concurrent local edit is not
	// silently dropped (issues #417 and #494).
	if err := e.clearDirtyAfterImport(ctx, calendarID, uid, res.etag, revs[uid]); err != nil {
		e.logger.Warn("clear post-import dirty", "uid", uid, "error", err)
	}

	e.logger.Debug("pulled resource", "uid", uid, "path", res.href, "etag", res.etag)
	return uid, true, warnings, nil
}

// dropTombstonedFromImport removes events/todos/journals whose UID is
// tombstoned for the calendar, so an accept-server import never resurrects a
// row the user has locally deleted. Returns the result unchanged when nothing
// is tombstoned.
func (e *Engine) dropTombstonedFromImport(ctx context.Context, calendarID int64, result icalPkg.ImportResult) (icalPkg.ImportResult, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return result, fmt.Errorf("list tombstones: %w", err)
	}
	if len(tombstones) == 0 {
		return result, nil
	}
	tombstoned := make(map[string]bool, len(tombstones))
	for _, ts := range tombstones {
		if ts.Uid != "" {
			tombstoned[ts.Uid] = true
		}
	}

	result.Events = filterTombstoned(e.logger, result.Events, tombstoned, ownerTypeEvent, func(ev event.Event) string { return ev.UID })
	result.Todos = filterTombstoned(e.logger, result.Todos, tombstoned, ownerTypeTodo, func(t todo.Todo) string { return t.UID })
	result.Journals = filterTombstoned(e.logger, result.Journals, tombstoned, ownerTypeJournal, func(j journal.Journal) string { return j.UID })

	return result, nil
}

// filterTombstoned returns items whose UID (via uidOf) is not tombstoned.
// It logs each one it drops. The result reuses a zero-capacity head of the
// input so append always allocates fresh and never clobbers the caller's slice.
func filterTombstoned[T any](logger *slog.Logger, items []T, tombstoned map[string]bool, ownerType string, uidOf func(T) string) []T {
	kept := items[:0:0]
	for _, it := range items {
		if uid := uidOf(it); tombstoned[uid] {
			logger.Info("skip accept-server import: UID tombstoned locally", "uid", uid, "owner_type", ownerType)
			continue
		}
		kept = append(kept, it)
	}
	return kept
}
