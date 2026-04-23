package event

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// DeleteKind discriminates the two reversible delete shapes the service can
// snapshot. Consumers (TUI undo stack) treat snapshots as opaque data; only
// Service.Restore interprets them.
type DeleteKind int

const (
	// DeleteKindFull covers the removal of an entire event row — standalone,
	// override, or solo master. Restore re-inserts the row and its nine
	// transient child slices and reconciles sync state.
	DeleteKindFull DeleteKind = iota
	// DeleteKindInstance covers "this occurrence only" on a recurring master.
	// No row is deleted; the master gains an EXDATE entry. Restore removes
	// that EXDATE so the occurrence reappears on next expansion.
	DeleteKindInstance
)

// DeletedSnapshot is a self-contained record of a just-completed delete,
// sufficient to reverse it via Service.Restore. Two flavors, discriminated by
// Kind: a full event-row delete (with hydrated transient children and
// optional master-state capture for overrides), or a single-instance delete
// against a recurring master (with the occurrence time plus master-state
// capture for drift detection).
//
// DeletedSnapshot is data, not behavior: callers keep it in memory without
// knowing how to restore it. Only the service decides how to reverse the
// delete.
type DeletedSnapshot struct {
	Kind DeleteKind

	// Full-delete fields (Kind == DeleteKindFull).

	// Event is the entire event row with its nine transient slices hydrated.
	Event Event

	// MasterExDatesAtDelete is the full EXDATE list on the master as observed
	// at delete time. Only populated when Event.RecurrenceID != "".
	MasterExDatesAtDelete string

	// MasterUpdatedAtAtDelete is the master's updated_at when delete ran.
	// Used to detect master drift during restore of an override.
	MasterUpdatedAtAtDelete time.Time

	// Instance-delete fields (Kind == DeleteKindInstance).

	// InstanceUID is the UID of the recurring master whose occurrence was
	// excluded.
	InstanceUID string
	// InstanceTime is the occurrence time (the value that was appended to the
	// master's EXDATE list).
	InstanceTime time.Time
	// InstanceCalendarID is the calendar owning the master; cached so the
	// undo path can mark the resource dirty without a second DB lookup.
	InstanceCalendarID int64
	// InstanceTitle carries the master title for display in the toast.
	InstanceTitle string
}

// IsValid reports whether the snapshot has enough content to be restored.
// The TUI uses this to decide whether to enqueue undo and show the toast.
func (s DeletedSnapshot) IsValid() bool {
	switch s.Kind {
	case DeleteKindFull:
		return s.Event.UID != ""
	case DeleteKindInstance:
		return s.InstanceUID != "" && !s.InstanceTime.IsZero()
	}
	return false
}

// EstimatedBytes returns a best-effort byte cost of this snapshot, used by
// the TUI undo stack to enforce a byte budget on top of the depth budget.
// Attachment blobs dominate for full deletes; instance deletes are tiny.
func (s DeletedSnapshot) EstimatedBytes() int {
	if s.Kind == DeleteKindInstance {
		return len(s.InstanceUID) + len(s.InstanceTitle) + 128
	}
	n := len(s.Event.Title) + len(s.Event.Description) + len(s.Event.Location)
	for _, a := range s.Event.Attachments {
		n += len(a.Data)
		n += len(a.URI)
	}
	return n + 256 // small constant for scalars / slice headers
}

// ErrMasterChanged is returned by Restore when reviving an override and the
// master has been updated since delete in a way that drops the override's
// RECURRENCE-ID from its EXDATE list. Silently removing a newer exclusion
// would overwrite a decision made on another device or via sync pull.
var ErrMasterChanged = errors.New("master event changed since delete; cannot safely restore override")

// DeleteWithSnapshot hydrates the nine transient child slices, captures a
// snapshot suitable for restore, then performs the normal Delete side effects
// (tombstone + EXDATE on override). It returns the snapshot so an undo stack
// can hold on to it.
func (s *Service) DeleteWithSnapshot(ctx context.Context, id int64) (DeletedSnapshot, error) {
	r, err := s.q.GetEvent(ctx, id)
	if err != nil {
		return DeletedSnapshot{}, err
	}
	evt := fromStorage(r)

	// Reject recurring masters with overrides early — matches Delete's
	// contract so callers get the same error before we load children.
	if evt.RecurrenceRule != "" && evt.RecurrenceID == "" {
		overrides, listErr := s.q.ListOverridesByUID(ctx, evt.UID)
		if listErr != nil {
			return DeletedSnapshot{}, fmt.Errorf("check overrides: %w", listErr)
		}
		if len(overrides) > 0 {
			return DeletedSnapshot{}, ErrHasOverrides
		}
	}

	// Hydrate transient children. Errors here are non-fatal for delete but
	// would leave the snapshot incomplete; surface them.
	if evt.Alarms, err = s.ListAlarms(ctx, evt.ID); err != nil {
		return DeletedSnapshot{}, fmt.Errorf("list alarms: %w", err)
	}
	if evt.Attendees, err = s.ListAttendees(ctx, evt.ID); err != nil {
		return DeletedSnapshot{}, fmt.Errorf("list attendees: %w", err)
	}
	if evt.Attachments, err = s.ListAttachments(ctx, evt.ID); err != nil {
		return DeletedSnapshot{}, fmt.Errorf("list attachments: %w", err)
	}
	if evt.Comments, err = s.ListComments(ctx, evt.ID); err != nil {
		return DeletedSnapshot{}, fmt.Errorf("list comments: %w", err)
	}
	if evt.Contacts, err = s.ListContacts(ctx, evt.ID); err != nil {
		return DeletedSnapshot{}, fmt.Errorf("list contacts: %w", err)
	}
	if evt.Resources, err = s.ListResources(ctx, evt.ID); err != nil {
		return DeletedSnapshot{}, fmt.Errorf("list resources: %w", err)
	}
	if evt.Relations, err = s.ListRelations(ctx, evt.ID); err != nil {
		return DeletedSnapshot{}, fmt.Errorf("list relations: %w", err)
	}
	if evt.XProperties, err = s.ListXProperties(ctx, evt.ID); err != nil {
		return DeletedSnapshot{}, fmt.Errorf("list x-properties: %w", err)
	}
	// Categories live on the Event value already (populated by the caller
	// paths that read Event), but for defensive hydration read them fresh.
	if cats, cerr := s.ListCategories(ctx, evt.ID); cerr == nil {
		evt.Categories = strings.Join(cats, ",")
	}

	snap := DeletedSnapshot{Event: evt}

	// For override deletes, capture master state before Delete mutates it.
	if evt.RecurrenceID != "" {
		master, mErr := s.q.GetEventByUID(ctx, evt.UID)
		if mErr == nil {
			snap.MasterExDatesAtDelete = storage.NullableToString(master.Exdates)
			if t, perr := time.Parse(time.RFC3339, master.UpdatedAt); perr == nil {
				snap.MasterUpdatedAtAtDelete = t
			}
		}
	}

	if err := s.Delete(ctx, id); err != nil {
		return DeletedSnapshot{}, err
	}

	return snap, nil
}

// DeleteInstanceWithSnapshot excludes a single occurrence of a recurring
// master by appending instanceTime to the master's EXDATE list, returning a
// snapshot that lets Restore reinstate the occurrence. The underlying work
// reuses Service.DeleteInstance; this wrapper only captures the master's
// pre-delete EXDATE list and UpdatedAt so a later restore can detect drift.
//
// Only the "this occurrence only" flavor of recurring delete is snapshotted.
// "This and following" (DeleteFromInstance) and "all events" (DeleteSeries)
// are out of scope for undo — they're multi-step destructive changes that
// the snapshot format does not yet carry.
func (s *Service) DeleteInstanceWithSnapshot(ctx context.Context, uid string, instanceTime time.Time) (DeletedSnapshot, error) {
	master, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return DeletedSnapshot{}, fmt.Errorf("get master: %w", err)
	}

	snap := DeletedSnapshot{
		Kind:                  DeleteKindInstance,
		InstanceUID:           uid,
		InstanceTime:          instanceTime,
		InstanceCalendarID:    master.CalendarID,
		InstanceTitle:         master.Title,
		MasterExDatesAtDelete: storage.NullableToString(master.Exdates),
	}
	if t, perr := time.Parse(time.RFC3339, master.UpdatedAt); perr == nil {
		snap.MasterUpdatedAtAtDelete = t
	}

	if err := s.DeleteInstance(ctx, uid, instanceTime); err != nil {
		return DeletedSnapshot{}, err
	}
	return snap, nil
}

// Restore reverses a DeleteWithSnapshot or DeleteInstanceWithSnapshot,
// dispatching on the snapshot's Kind. For full deletes it re-inserts the
// event row with its original UID and recurrence_id, re-populating the nine
// transient child tables, and reconciling sync state via a three-case state
// machine:
//
//  1. Local-only calendar / resource never uploaded: no sync work.
//  2. Tombstone exists: delete the tombstone (delete hasn't been pushed).
//  3. Tombstone gone AND sync_resource gone: delete was already pushed
//     upstream; mark the restored resource dirty so the next push recreates
//     it at a fresh remote href.
//
// For overrides, Restore removes the matching RECURRENCE-ID from the
// master's current EXDATE list. If the master has advanced since delete
// (updated_at moved forward) and no longer carries that exclusion, the
// restore aborts with ErrMasterChanged rather than silently contradict a
// newer decision made on another device.
//
// Restore uses a plain INSERT (not upsert): if a different row now occupies
// (uid, recurrence_id), the unique constraint fails and the caller learns
// that the slot has been reclaimed.
//
// For instance deletes (Kind == DeleteKindInstance), Restore removes the
// matching occurrence from the master's current EXDATE list. If the master
// has advanced since delete (updated_at moved forward) and no longer carries
// that exclusion, the restore aborts with ErrMasterChanged.
func (s *Service) Restore(ctx context.Context, snap DeletedSnapshot) (int64, error) {
	if snap.Kind == DeleteKindInstance {
		return 0, s.restoreInstance(ctx, snap)
	}
	evt := snap.Event
	if evt.UID == "" {
		return 0, fmt.Errorf("restore: empty UID")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Override branch: patch the master's EXDATE before inserting the override.
	if evt.RecurrenceID != "" {
		master, mErr := qtx.GetEventByUID(ctx, evt.UID)
		if mErr != nil {
			return 0, fmt.Errorf("restore override: master missing: %w", mErr)
		}
		recIDTime, parseErr := timeutil.ParseRecurrenceID(evt.RecurrenceID)
		if parseErr != nil {
			return 0, fmt.Errorf("parse recurrence id: %w", parseErr)
		}
		currentExDates := ParseTimeList(storage.NullableToString(master.Exdates))
		filtered := make([]time.Time, 0, len(currentExDates))
		found := false
		for _, t := range currentExDates {
			if t.Equal(recIDTime) {
				found = true
				continue
			}
			filtered = append(filtered, t)
		}
		if !found {
			// Master no longer carries this EXDATE. If the master has been
			// updated since our snapshot, a sync pull or local edit already
			// decided this instance is live again — refuse to overwrite.
			if masterUpdated, perr := time.Parse(time.RFC3339, master.UpdatedAt); perr == nil {
				if masterUpdated.After(snap.MasterUpdatedAtAtDelete) {
					return 0, ErrMasterChanged
				}
			}
			// Snapshot master matches current master and neither has the
			// EXDATE — safe to proceed (nothing to remove).
		} else {
			if err := qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
				Exdates: storage.StringToNullable(SerializeTimeList(filtered)),
				ID:      master.ID,
			}); err != nil {
				return 0, fmt.Errorf("update master exdates: %w", err)
			}
		}
	}

	// Re-insert the event row with preserved UID + recurrence_id. Let the
	// unique constraint fail if a new event has since claimed that slot.
	row, err := qtx.CreateEvent(ctx, storage.CreateEventParams{
		Uid:            evt.UID,
		CalendarID:     evt.CalendarID,
		Title:          evt.Title,
		Description:    storage.StringToNullable(evt.Description),
		Location:       storage.StringToNullable(evt.Location),
		StartTime:      evt.StartTime.Format(time.RFC3339),
		EndTime:        evt.EndTime.Format(time.RFC3339),
		AllDay:         storage.BoolToInt(evt.AllDay),
		RecurrenceRule: storage.StringToNullable(evt.RecurrenceRule),
		Timezone:       storage.StringToNullable(evt.Timezone),
		Status:         evt.Status,
		Transp:         evt.Transp,
		Sequence:       evt.Sequence,
		Priority:       evt.Priority,
		Class:          evt.Class,
		Url:            storage.StringToNullable(evt.URL),
		Exdates:        storage.StringToNullable(evt.ExDates),
		Rdates:         storage.StringToNullable(evt.RDates),
		RecurrenceID:   evt.RecurrenceID,
		Geo:            storage.StringToNullable(evt.Geo),
		Duration:       storage.StringToNullable(evt.DurationValue),
		Dtstamp:        storage.StringToNullable(evt.DtStamp),
		ConferenceUri:  evt.ConferenceURI,
	})
	if err != nil {
		return 0, fmt.Errorf("restore event row: %w", err)
	}
	newID := row.ID

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// Transient children: use the existing Replace* helpers, each of which
	// opens its own TX. Errors here leave the event restored but incomplete;
	// surface them so the caller can decide how to display failure.
	if len(evt.Alarms) > 0 {
		if err := s.ReplaceAlarms(ctx, newID, evt.Alarms); err != nil {
			return newID, fmt.Errorf("restore alarms: %w", err)
		}
	}
	if len(evt.Attendees) > 0 {
		if err := s.ReplaceAttendees(ctx, newID, evt.Attendees); err != nil {
			return newID, fmt.Errorf("restore attendees: %w", err)
		}
	}
	if len(evt.Attachments) > 0 {
		if err := s.ReplaceAttachments(ctx, newID, evt.Attachments); err != nil {
			return newID, fmt.Errorf("restore attachments: %w", err)
		}
	}
	if len(evt.Comments) > 0 {
		if err := s.ReplaceComments(ctx, newID, evt.Comments); err != nil {
			return newID, fmt.Errorf("restore comments: %w", err)
		}
	}
	if len(evt.Contacts) > 0 {
		if err := s.ReplaceContacts(ctx, newID, evt.Contacts); err != nil {
			return newID, fmt.Errorf("restore contacts: %w", err)
		}
	}
	if len(evt.Resources) > 0 {
		if err := s.ReplaceResources(ctx, newID, evt.Resources); err != nil {
			return newID, fmt.Errorf("restore resources: %w", err)
		}
	}
	if len(evt.Relations) > 0 {
		if err := s.ReplaceRelations(ctx, newID, evt.Relations); err != nil {
			return newID, fmt.Errorf("restore relations: %w", err)
		}
	}
	if len(evt.XProperties) > 0 {
		if err := s.ReplaceXProperties(ctx, newID, evt.XProperties); err != nil {
			return newID, fmt.Errorf("restore x-properties: %w", err)
		}
	}
	if cats := ParseCategoryList(evt.Categories); len(cats) > 0 {
		if err := s.ReplaceCategories(ctx, newID, cats); err != nil {
			return newID, fmt.Errorf("restore categories: %w", err)
		}
	}

	// Sync reconciliation — 3-case state machine.
	// Case A: local-only calendar or no sync_resource → MarkResourceDirty is
	// a no-op and no tombstone exists, so the below operations are both safe
	// no-ops.
	// Case B: tombstone exists (delete not yet pushed) → clear it; the
	// sync_resource row still carries the old remote_url, which next push
	// will re-PUT to.
	// Case C: tombstone and sync_resource both gone (delete already pushed)
	// → MarkResourceDirty recreates sync_resource with remote_url='' and
	// dirty=1, so next push allocates a fresh href.
	_ = s.q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{
		CalendarID: evt.CalendarID,
		Uid:        evt.UID,
	})
	_ = storage.MarkResourceDirty(ctx, s.db, evt.CalendarID, evt.UID, "event")

	// Override restore also marks the master dirty because we mutated its
	// EXDATE.
	if evt.RecurrenceID != "" {
		_ = storage.MarkResourceDirty(ctx, s.db, evt.CalendarID, evt.UID, "event")
	}

	return newID, nil
}


// restoreInstance reverses DeleteInstanceWithSnapshot by removing the
// snapshotted occurrence time from the master's current EXDATE list. When
// the occurrence is already absent and the master has advanced since the
// snapshot, it returns ErrMasterChanged so the caller can surface the
// conflict instead of silently overwriting a newer decision.
func (s *Service) restoreInstance(ctx context.Context, snap DeletedSnapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	master, err := qtx.GetEventByUID(ctx, snap.InstanceUID)
	if err != nil {
		return fmt.Errorf("restore instance: master missing: %w", err)
	}

	currentExDates := ParseTimeList(storage.NullableToString(master.Exdates))
	filtered := make([]time.Time, 0, len(currentExDates))
	found := false
	for _, t := range currentExDates {
		if t.Equal(snap.InstanceTime) {
			found = true
			continue
		}
		filtered = append(filtered, t)
	}
	if !found {
		// Master doesn't carry this EXDATE anymore. Either it never did
		// (odd — our own DeleteInstance put it there) or a sync pull /
		// external edit removed it after the snapshot. If the master has
		// advanced beyond snapshot time, respect that decision.
		if masterUpdated, perr := time.Parse(time.RFC3339, master.UpdatedAt); perr == nil {
			if masterUpdated.After(snap.MasterUpdatedAtAtDelete) {
				return ErrMasterChanged
			}
		}
		// Master matches snapshot and neither has the EXDATE — nothing to
		// remove, restoration is vacuously complete.
		return tx.Commit()
	}

	if err := qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
		Exdates: storage.StringToNullable(SerializeTimeList(filtered)),
		ID:      master.ID,
	}); err != nil {
		return fmt.Errorf("update master exdates: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	_ = storage.MarkResourceDirty(ctx, s.db, master.CalendarID, snap.InstanceUID, "event")
	return nil
}
