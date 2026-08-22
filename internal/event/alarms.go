package event

import (
	"context"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// Alarm CRUD

// buildAlarmsWithAttendees converts storage alarm rows into model.Alarm
// values with attendees batch-loaded.
func buildAlarmsWithAttendees(ctx context.Context, q *storage.Queries, rows []storage.EventAlarm) ([]model.Alarm, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	alarmIDs := make([]int64, len(rows))
	for i, r := range rows {
		alarmIDs[i] = r.ID
	}
	// Load failures propagate: attendees feed content matching in
	// ReplaceAlarms and X-properties feed export/sync pushes, so a silently
	// degraded alarm set would corrupt merges or rewrite the server copy.
	attRows, err := q.ListAlarmAttendeesByAlarmIDs(ctx, alarmIDs)
	if err != nil {
		return nil, fmt.Errorf("load alarm attendees: %w", err)
	}
	attMap := make(map[int64][]model.AlarmAttendee, len(rows))
	for _, ar := range attRows {
		attMap[ar.AlarmID] = append(attMap[ar.AlarmID], model.AlarmAttendee{
			ID: ar.ID, Email: ar.Email, Name: storage.NullableToString(ar.Name),
		})
	}
	alarms := make([]model.Alarm, len(rows))
	for i, r := range rows {
		alarms[i] = fromStorageAlarm(r)
		alarms[i].Attendees = attMap[r.ID]
	}
	if err := storage.AttachAlarmXProperties(ctx, q, storage.OwnerTypeEventAlarm, alarms); err != nil {
		return nil, err
	}
	return alarms, nil
}

func (s *Service) ListAlarms(ctx context.Context, eventID int64) ([]model.Alarm, error) {
	rows, err := s.q.ListAlarmsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	return buildAlarmsWithAttendees(ctx, s.q, rows)
}

// ListFireableAlarmsByEventIDs fetches the fireable alarms for multiple
// event IDs in a single batch query. Returns a map of event ID to its list
// of alarms. The query excludes preserved sync-only actions (issue #579):
// the alarm check loop is the only caller, and a sync-only alarm must not
// reach it.
func (s *Service) ListFireableAlarmsByEventIDs(ctx context.Context, eventIDs []int64) (map[int64][]model.Alarm, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	alarmRows, err := s.q.ListFireableAlarmsByEventIDs(ctx, eventIDs)
	if err != nil {
		return nil, err
	}
	alarms, err := buildAlarmsWithAttendees(ctx, s.q, alarmRows)
	if err != nil {
		return nil, err
	}
	if len(alarms) == 0 {
		return nil, nil
	}
	alarmMap := make(map[int64][]model.Alarm, len(eventIDs))
	for _, a := range alarms {
		alarmMap[a.EventID] = append(alarmMap[a.EventID], a)
	}
	return alarmMap, nil
}

// loadExistingAlarms loads alarms that already exist, with their attendees, for the given event.
func loadExistingAlarms(ctx context.Context, qtx *storage.Queries, eventID int64) ([]model.Alarm, error) {
	rows, err := qtx.ListAlarmsByEventID(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("list existing alarms: %w", err)
	}
	return buildAlarmsWithAttendees(ctx, qtx, rows)
}

// matchAlarm tries to match a new alarm with stored ones by content.
// Returns true and the index if matched, false otherwise. Rows whose
// non-empty RFC 9074 UIDs differ are never paired. The UID identifies the
// alarm. A content coincidence across different UIDs would attach
// alarm_state to the wrong definition and churn UIDs on the server.
func matchAlarm(existing []model.Alarm, matched []bool, a model.Alarm) (int, bool) {
	for j, ex := range existing {
		if matched[j] {
			continue
		}
		if a.UID != "" && ex.UID != "" && a.UID != ex.UID {
			continue
		}
		if a.ContentEqual(ex) {
			return j, true
		}
	}
	return 0, false
}

// alarmUID returns the UID this service stores for a. See
// model.AlarmUIDForWrite for the rule, which the todo service shares.
func alarmUID(a model.Alarm) string {
	return model.AlarmUIDForWrite(a)
}

// matchAlarmByUID tries to match a new alarm with stored ones by
// RFC 9074 UID. Use this as a fallback when content match fails. An edited
// alarm (for example a changed trigger) then updates its row in place instead
// of a delete and re-create. A delete would cascade away its alarm_state and
// restore dismissed firings.
func matchAlarmByUID(existing []model.Alarm, matched []bool, a model.Alarm) (int, bool) {
	if a.UID == "" {
		return 0, false
	}
	for j, ex := range existing {
		if matched[j] || ex.UID == "" {
			continue
		}
		if ex.UID == a.UID {
			return j, true
		}
	}
	return 0, false
}

// updateAlarmInPlace rewrites a UID-matched alarm's content on its stored
// row. The row ID stays so alarm_state entries keyed to it survive.
func updateAlarmInPlace(ctx context.Context, qtx *storage.Queries, eventID int64, a model.Alarm, ex model.Alarm) error {
	// A rewrite to a sync-only action disables the alarm, but this code
	// leaves alarm_state alone. The pending and snooze queries filter on
	// the current action, so the state of the alarm comes back when a
	// later pull restores a fireable action (issue #579).
	ack, err := model.PrepareAlarmUpdate(a, ex)
	if err != nil {
		return err
	}
	if err := qtx.UpdateAlarmContentByID(ctx, storage.UpdateAlarmContentByIDParams{
		Action:        a.Action,
		TriggerValue:  a.TriggerValue,
		Description:   storage.StringToNullable(a.Description),
		Summary:       storage.StringToNullable(a.Summary),
		Repeat:        int64(a.Repeat),
		Duration:      storage.StringToNullable(a.Duration),
		Related:       a.Related,
		Acknowledged:  storage.StringToNullable(ack),
		AttachUri:     storage.StringToNullable(a.AttachURI),
		AttachFmttype: storage.StringToNullable(a.AttachFmtType),
		AttachBinary:  a.AttachBinary,
		ID:            ex.ID,
		EventID:       eventID,
	}); err != nil {
		return fmt.Errorf("update alarm content: %w", err)
	}
	if err := qtx.DeleteAlarmAttendeesByAlarmID(ctx, ex.ID); err != nil {
		return fmt.Errorf("delete alarm attendees: %w", err)
	}
	for _, att := range a.Attendees {
		_, err := qtx.CreateAlarmAttendee(ctx, storage.CreateAlarmAttendeeParams{
			AlarmID: ex.ID,
			Email:   att.Email,
			Name:    storage.StringToNullable(att.Name),
		})
		if err != nil {
			return fmt.Errorf("create alarm attendee: %w", err)
		}
	}
	if a.XProperties == nil || model.XPropsContentEqual(a.XProperties, ex.XProperties) {
		return nil
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeEventAlarm, ex.ID, a.XProperties)
}

// syncMatchedAlarm syncs a matched alarm's UID and ACKNOWLEDGED state.
func syncMatchedAlarm(ctx context.Context, qtx *storage.Queries, eventID int64, a model.Alarm, ex model.Alarm) error {
	// Backfill the UID of a stored row that carries none. A preserved
	// foreign alarm keeps its empty UID, so the backfill writes nothing
	// for it (issue #586).
	if uid := alarmUID(a); ex.UID == "" && uid != "" {
		if err := qtx.UpdateAlarmUID(ctx, storage.UpdateAlarmUIDParams{
			Uid: storage.StringToNullable(uid),
			ID:  ex.ID,
		}); err != nil {
			return fmt.Errorf("backfill alarm uid: %w", err)
		}
	}
	// Sync ACKNOWLEDGED if the incoming value differs (including clearing).
	if a.Acknowledged != ex.Acknowledged && model.ValidateAcknowledged(a.Acknowledged) {
		if err := qtx.UpdateAlarmAcknowledged(ctx, storage.UpdateAlarmAcknowledgedParams{
			Acknowledged: storage.StringToNullable(a.Acknowledged),
			ID:           ex.ID,
			EventID:      eventID,
		}); err != nil {
			return fmt.Errorf("update alarm acknowledged: %w", err)
		}
	}
	// X-properties are excluded from content matching; refresh them so a
	// remote X-prop change still lands. nil means the caller has no X-prop
	// knowledge (CLI flags, TUI fallback paths) — keep the stored rows; only
	// a non-nil slice (import/sync always populates one) is authoritative.
	if a.XProperties == nil || model.XPropsContentEqual(a.XProperties, ex.XProperties) {
		return nil
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeEventAlarm, ex.ID, a.XProperties)
}

// isUniqueUIDViolation reports whether an insert failed on the global
// alarm-UID unique index.
func isUniqueUIDViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") && strings.Contains(msg, ".uid")
}

// createNewAlarm creates a new alarm and its attendees. Alarm UIDs are
// globally unique. Servers sometimes duplicate an event (same VALARM UIDs on
// both copies), which would otherwise fail this event's sync forever. On
// collision, mint a fresh local UID instead.
func createNewAlarm(ctx context.Context, qtx *storage.Queries, eventID int64, a model.Alarm) error {
	if err := model.CheckStorableAlarmAction(a.Action); err != nil {
		return fmt.Errorf("create alarm: %w", err)
	}
	params := storage.CreateAlarmParams{
		EventID:       eventID,
		Uid:           storage.StringToNullable(alarmUID(a)),
		Action:        a.Action,
		TriggerValue:  a.TriggerValue,
		Description:   storage.StringToNullable(a.Description),
		Summary:       storage.StringToNullable(a.Summary),
		Repeat:        int64(a.Repeat),
		Duration:      storage.StringToNullable(a.Duration),
		Related:       a.Related,
		Acknowledged:  storage.StringToNullable(a.Acknowledged),
		AttachUri:     storage.StringToNullable(a.AttachURI),
		AttachFmttype: storage.StringToNullable(a.AttachFmtType),
		AttachBinary:  a.AttachBinary,
	}
	row, err := qtx.CreateAlarm(ctx, params)
	if isUniqueUIDViolation(err) {
		// A server can send the same VALARM UID on two resources. Retry
		// through the shared rule, so the retry does not stamp a minted
		// UID on a preserved foreign alarm (issue #586). The rule mints
		// for a fireable action and yields an empty value otherwise.
		retry := a
		retry.UID = ""
		params.Uid = storage.StringToNullable(model.AlarmUIDForWrite(retry))
		row, err = qtx.CreateAlarm(ctx, params)
	}
	if err != nil {
		return fmt.Errorf("create alarm: %w", err)
	}
	for _, att := range a.Attendees {
		_, err := qtx.CreateAlarmAttendee(ctx, storage.CreateAlarmAttendeeParams{
			AlarmID: row.ID,
			Email:   att.Email,
			Name:    storage.StringToNullable(att.Name),
		})
		if err != nil {
			return fmt.Errorf("create alarm attendee: %w", err)
		}
	}
	if len(a.XProperties) == 0 {
		return nil
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeEventAlarm, row.ID, a.XProperties)
}

// deleteUnmatchedAlarms deletes alarms that were not matched.
func deleteUnmatchedAlarms(ctx context.Context, qtx *storage.Queries, existing []model.Alarm, matched []bool) error {
	for j, ex := range existing {
		if !matched[j] {
			if err := qtx.DeleteAlarmByID(ctx, ex.ID); err != nil {
				return fmt.Errorf("delete unmatched alarm: %w", err)
			}
		}
	}
	return nil
}

// ReplaceFireableAlarms replaces the fireable alarms of an event and
// carries the stored sync-only rows forward. A caller that cannot state a
// preserved action — the CLI --alarm flag — uses this method, so a
// routine edit does not delete the VALARM of another client (issue #579).
// A caller that must delete such a row calls ReplaceAlarms instead.
// The read of the stored rows and the write share one transaction. A read
// on its own connection could return a row a concurrent pull has already
// deleted. The carry-over would then write that row again, and the next
// push would restore the deleted VALARM on the server.
func (s *Service) ReplaceFireableAlarms(ctx context.Context, eventID int64, alarms []model.Alarm) error {
	if s.tx != nil {
		return s.replaceFireableAlarms(ctx, eventID, alarms)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.WithTx(tx).replaceFireableAlarms(ctx, eventID, alarms); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceFireableAlarms carries the stored sync-only rows forward. The
// caller supplies the transaction.
func (s *Service) replaceFireableAlarms(ctx context.Context, eventID int64, alarms []model.Alarm) error {
	stored, err := s.ListAlarms(ctx, eventID)
	if err != nil {
		return fmt.Errorf("list stored alarms: %w", err)
	}
	return s.ReplaceAlarms(ctx, eventID, model.KeepSyncOnlyAlarms(stored, alarms))
}

// ClearSyncOnlyAlarms deletes every stored alarm the engine cannot fire and
// keeps the rest. ReplaceFireableAlarms carries those rows forward, so a
// --alarm edit alone can never remove one. This method gives the user a way
// out on a local calendar (issue #593).
//
// The read of the stored rows and the write share one transaction, like
// ReplaceFireableAlarms.
func (s *Service) ClearSyncOnlyAlarms(ctx context.Context, eventID int64) error {
	if s.tx != nil {
		return s.clearSyncOnlyAlarms(ctx, eventID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.WithTx(tx).clearSyncOnlyAlarms(ctx, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

// clearSyncOnlyAlarms keeps the fireable stored rows. The caller supplies
// the transaction.
func (s *Service) clearSyncOnlyAlarms(ctx context.Context, eventID int64) error {
	stored, err := s.ListAlarms(ctx, eventID)
	if err != nil {
		return fmt.Errorf("list stored alarms: %w", err)
	}
	return s.ReplaceAlarms(ctx, eventID, model.FireableAlarmsOnly(stored))
}

func (s *Service) ReplaceAlarms(ctx context.Context, eventID int64, alarms []model.Alarm) error {
	if err := s.ensureEventWritable(ctx, eventID, 0); err != nil {
		return err
	}
	return s.ReplaceAlarmsForSync(ctx, eventID, alarms)
}

// ReplaceAlarmsForSync applies an alarm set without the remote access and
// component policy. It is reserved for the CalDAV sync engine, which
// mirrors a server-originated VEVENT into the local cache whatever the
// linked collection advertises. A user-originated edit must route through
// ReplaceAlarms, so the policy holds.
func (s *Service) ReplaceAlarmsForSync(ctx context.Context, eventID int64, alarms []model.Alarm) error {
	// Prepare before the transaction opens. A standalone call then
	// rejects a bad alarm without a write lock. A sync caller already
	// holds its own transaction. See model.PrepareAlarmsForWrite.
	alarms, err := model.PrepareAlarmsForWrite(alarms)
	if err != nil {
		return err
	}
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	if err := replaceAlarmsTx(ctx, qtx, eventID, alarms); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

// replaceAlarmsTx reconciles an event's alarms (content/UID match, in-place
// edits, creates, deletes) using a tx-bound Queries. It opens no transaction so
// callers can compose it with the event row write inside one transaction.
func replaceAlarmsTx(ctx context.Context, qtx *storage.Queries, eventID int64, alarms []model.Alarm) error {
	// Precondition: the caller prepares alarms with
	// model.PrepareAlarmsForWrite. Both callers in this file do.
	// Load existing alarms with attendees for content matching.
	existing, err := loadExistingAlarms(ctx, qtx, eventID)
	if err != nil {
		return err
	}

	// Match incoming alarms against existing by content.
	// Slice-based matching: each existing alarm can only match once (supports duplicates).
	matched := make([]bool, len(existing))
	var unmatched []model.Alarm
	for _, a := range alarms {
		if j, found := matchAlarm(existing, matched, a); found {
			matched[j] = true
			if err := syncMatchedAlarm(ctx, qtx, eventID, a, existing[j]); err != nil {
				return err
			}
		} else {
			unmatched = append(unmatched, a)
		}
	}

	// Second pass: alarms whose content changed but whose RFC 9074 UID is
	// stable are the same alarm edited, not a new one. Update in place so
	// the row ID — and the alarm_state rows hanging off it — survive.
	for _, a := range unmatched {
		if j, found := matchAlarmByUID(existing, matched, a); found {
			matched[j] = true
			if err := updateAlarmInPlace(ctx, qtx, eventID, a, existing[j]); err != nil {
				return err
			}
		} else {
			if err := createNewAlarm(ctx, qtx, eventID, a); err != nil {
				return err
			}
		}
	}

	// Delete existing alarms that were not matched (they were removed).
	return deleteUnmatchedAlarms(ctx, qtx, existing, matched)
}

// replaceRelationsTx replaces an event's attendees and alarms using a tx-bound
// Queries. The *WithRelations methods can then write both child collections
// inside the same transaction as the event row.
func replaceRelationsTx(ctx context.Context, qtx *storage.Queries, eventID int64, attendees []model.Attendee, alarms []model.Alarm) error {
	// The *WithRelations methods prepare the attendees and the alarms at
	// method entry.
	if err := replaceAttendeesTx(ctx, qtx, eventID, attendees); err != nil {
		return err
	}
	return replaceAlarmsTx(ctx, qtx, eventID, alarms)
}

// fromStorageAlarm converts an alarm row to the model value. It maps a
// malformed stored action to model.UnsupportedAlarmAction, so every
// reader holds a value the write rule accepts. See
// model.NormalizeAlarmAction for the rule (issue #607).
func fromStorageAlarm(r storage.EventAlarm) model.Alarm {
	return model.Alarm{
		ID:            r.ID,
		EventID:       r.EventID,
		UID:           storage.NullableToString(r.Uid),
		Action:        model.NormalizeAlarmAction(r.Action),
		TriggerValue:  r.TriggerValue,
		Description:   storage.NullableToString(r.Description),
		Summary:       storage.NullableToString(r.Summary),
		Repeat:        int(r.Repeat),
		Duration:      storage.NullableToString(r.Duration),
		Related:       r.Related,
		Acknowledged:  storage.NullableToString(r.Acknowledged),
		AttachURI:     storage.NullableToString(r.AttachUri),
		AttachBinary:  r.AttachBinary,
		AttachFmtType: storage.NullableToString(r.AttachFmttype),
	}
}
