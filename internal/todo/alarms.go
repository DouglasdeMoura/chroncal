package todo

import (
	"context"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// Alarm CRUD

func (s *Service) ListAlarms(ctx context.Context, todoID int64) ([]model.Alarm, error) {
	return s.listAlarms(ctx, todoID, true)
}

// ListAlarmsLean returns a todo's alarms with attendees (needed to fire
// EMAIL alarms) but without X-properties, which are round-trip-only and
// never read at fire time.
func (s *Service) ListAlarmsLean(ctx context.Context, todoID int64) ([]model.Alarm, error) {
	return s.listAlarms(ctx, todoID, false)
}

// ListFireableAlarmsByTodoIDs fetches the fireable alarms for many todo
// IDs in one query. It returns a map of the todo ID to its alarms. The
// alarm check loop reads every todo on each tick, so a call per todo
// costs one query per todo (issue #586). The query excludes a preserved
// sync-only action.
func (s *Service) ListFireableAlarmsByTodoIDs(ctx context.Context, todoIDs []int64) (map[int64][]model.Alarm, error) {
	if len(todoIDs) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListFireableTodoAlarmsByTodoIDs(ctx, todoIDs)
	if err != nil {
		return nil, err
	}
	alarms, err := s.buildAlarms(ctx, rows, false)
	if err != nil {
		return nil, err
	}
	if len(alarms) == 0 {
		return nil, nil
	}
	// fromStorageTodoAlarm maps the todo ID onto the EventID field of the
	// shared alarm model, like the event service does with its own ID.
	alarmMap := make(map[int64][]model.Alarm, len(todoIDs))
	for _, a := range alarms {
		alarmMap[a.EventID] = append(alarmMap[a.EventID], a)
	}
	return alarmMap, nil
}

func (s *Service) listAlarms(ctx context.Context, todoID int64, withXProps bool) ([]model.Alarm, error) {
	rows, err := s.q.ListTodoAlarmsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	return s.buildAlarms(ctx, rows, withXProps)
}

// buildAlarms turns todo_alarms rows into the shared alarm model. It
// attaches the attendees, and the X-properties when the caller asks for
// them. The per-todo read and the batch read share it.
func (s *Service) buildAlarms(ctx context.Context, rows []storage.TodoAlarm, withXProps bool) ([]model.Alarm, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	alarmIDs := make([]int64, len(rows))
	for i, r := range rows {
		alarmIDs[i] = r.ID
	}
	// Load failures propagate. Attendees feed content match in ReplaceAlarms.
	// X-properties feed export and sync pushes. A silent degraded alarm set
	// would corrupt merges or rewrite the server copy.
	attRows, err := s.q.ListTodoAlarmAttendeesByAlarmIDs(ctx, alarmIDs)
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
		alarms[i] = fromStorageTodoAlarm(r)
		alarms[i].Attendees = attMap[r.ID]
	}
	if withXProps {
		if err := storage.AttachAlarmXProperties(ctx, s.q, storage.OwnerTypeTodoAlarm, alarms); err != nil {
			return nil, err
		}
	}
	return alarms, nil
}

// fromStorageTodoAlarm converts an alarm row to the model value. It maps
// a malformed stored action to model.UnsupportedAlarmAction, like the
// event service does. See model.NormalizeAlarmAction (issue #607).
func fromStorageTodoAlarm(r storage.TodoAlarm) model.Alarm {
	return model.Alarm{
		ID: r.ID, EventID: r.TodoID,
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

// ReplaceFireableAlarms replaces the fireable alarms of a todo and
// carries the stored sync-only rows forward, like the event method of the
// same name (issue #579). A caller that must delete such a row calls
// ReplaceAlarms instead.
// The read of the stored rows and the write share one transaction, like
// the event method of the same name. A read on its own connection could
// return a row a concurrent pull has already deleted.
func (s *Service) ReplaceFireableAlarms(ctx context.Context, todoID int64, alarms []model.Alarm) error {
	if s.tx != nil {
		return s.replaceFireableAlarms(ctx, todoID, alarms)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.WithTx(tx).replaceFireableAlarms(ctx, todoID, alarms); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceFireableAlarms carries the stored sync-only rows forward. The
// caller supplies the transaction.
func (s *Service) replaceFireableAlarms(ctx context.Context, todoID int64, alarms []model.Alarm) error {
	stored, err := s.ListAlarms(ctx, todoID)
	if err != nil {
		return fmt.Errorf("list stored alarms: %w", err)
	}
	return s.ReplaceAlarms(ctx, todoID, model.KeepSyncOnlyAlarms(stored, alarms))
}

// ClearSyncOnlyAlarms deletes every stored alarm the engine cannot fire and
// keeps the rest, like the event method of the same name (issue #593). The
// read of the stored rows and the write share one transaction.
func (s *Service) ClearSyncOnlyAlarms(ctx context.Context, todoID int64) error {
	if s.tx != nil {
		return s.clearSyncOnlyAlarms(ctx, todoID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.WithTx(tx).clearSyncOnlyAlarms(ctx, todoID); err != nil {
		return err
	}
	return tx.Commit()
}

// clearSyncOnlyAlarms keeps the fireable stored rows. The caller supplies
// the transaction.
func (s *Service) clearSyncOnlyAlarms(ctx context.Context, todoID int64) error {
	stored, err := s.ListAlarms(ctx, todoID)
	if err != nil {
		return fmt.Errorf("list stored alarms: %w", err)
	}
	return s.ReplaceAlarms(ctx, todoID, model.FireableAlarmsOnly(stored))
}

func (s *Service) ReplaceAlarms(ctx context.Context, todoID int64, alarms []model.Alarm) error {
	td, err := s.Get(ctx, todoID)
	if err != nil {
		return err
	}
	if err := calendaraccess.EnsureWritable(ctx, s.q, td.CalendarID, todoComponent); err != nil {
		return err
	}
	return s.ReplaceAlarmsForSync(ctx, todoID, alarms)
}

// ReplaceAlarmsForSync applies an alarm set without the remote access and
// component policy. It is reserved for the CalDAV sync engine, which
// mirrors a server-originated VTODO into the local cache whatever the
// linked collection advertises. A user-originated edit must route through
// ReplaceAlarms, so the policy holds.
func (s *Service) ReplaceAlarmsForSync(ctx context.Context, todoID int64, alarms []model.Alarm) error {
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

	// Merge. Do not wipe and recreate. A delete of a todo alarm row cascades
	// away its todo_alarm_state. That would restore dismissed firings on
	// every sync rewrite. Mirrors event.Service.ReplaceAlarms.
	existing, err := loadExistingTodoAlarms(ctx, qtx, todoID)
	if err != nil {
		return err
	}

	matched := make([]bool, len(existing))
	var unmatched []model.Alarm
	for _, a := range alarms {
		if j, found := matchTodoAlarm(existing, matched, a); found {
			matched[j] = true
			if err := syncMatchedTodoAlarm(ctx, qtx, a, existing[j]); err != nil {
				return err
			}
		} else {
			unmatched = append(unmatched, a)
		}
	}

	// Second pass: content changed but the RFC 9074 UID is stable — the same
	// alarm edited. Update in place so the row ID (and its state) survives.
	for _, a := range unmatched {
		if j, found := matchTodoAlarmByUID(existing, matched, a); found {
			matched[j] = true
			if err := updateTodoAlarmInPlace(ctx, qtx, todoID, a, existing[j]); err != nil {
				return err
			}
		} else {
			if err := createNewTodoAlarm(ctx, qtx, todoID, a); err != nil {
				return err
			}
		}
	}

	for j, ex := range existing {
		if !matched[j] {
			if err := qtx.DeleteTodoAlarmByID(ctx, ex.ID); err != nil {
				return fmt.Errorf("delete unmatched alarm: %w", err)
			}
		}
	}

	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

// loadExistingTodoAlarms loads a todo's alarms with attendees and
// X-properties inside the transaction for merge match.
func loadExistingTodoAlarms(ctx context.Context, qtx *storage.Queries, todoID int64) ([]model.Alarm, error) {
	rows, err := qtx.ListTodoAlarmsByTodoID(ctx, todoID)
	if err != nil {
		return nil, fmt.Errorf("list existing alarms: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	alarmIDs := make([]int64, len(rows))
	for i, r := range rows {
		alarmIDs[i] = r.ID
	}
	attRows, err := qtx.ListTodoAlarmAttendeesByAlarmIDs(ctx, alarmIDs)
	if err != nil {
		return nil, fmt.Errorf("list alarm attendees: %w", err)
	}
	attMap := make(map[int64][]model.AlarmAttendee, len(rows))
	for _, ar := range attRows {
		attMap[ar.AlarmID] = append(attMap[ar.AlarmID], model.AlarmAttendee{
			ID: ar.ID, Email: ar.Email, Name: storage.NullableToString(ar.Name),
		})
	}
	alarms := make([]model.Alarm, len(rows))
	for i, r := range rows {
		alarms[i] = fromStorageTodoAlarm(r)
		alarms[i].Attendees = attMap[r.ID]
	}
	if err := storage.AttachAlarmXProperties(ctx, qtx, storage.OwnerTypeTodoAlarm, alarms); err != nil {
		return nil, err
	}
	return alarms, nil
}

// matchTodoAlarm tries to match an alarm that arrives with stored ones by
// content. Rows whose non-empty RFC 9074 UIDs differ are never paired.
// See event.matchAlarm for the rationale.
func matchTodoAlarm(existing []model.Alarm, matched []bool, a model.Alarm) (int, bool) {
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

// matchTodoAlarmByUID matches an alarm that arrives against unmatched stored
// ones by RFC 9074 UID.
func matchTodoAlarmByUID(existing []model.Alarm, matched []bool, a model.Alarm) (int, bool) {
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

// syncMatchedTodoAlarm syncs a content-matched alarm's UID and ACKNOWLEDGED state.
func syncMatchedTodoAlarm(ctx context.Context, qtx *storage.Queries, a model.Alarm, ex model.Alarm) error {
	// Backfill the UID of a stored row that carries none. A preserved
	// foreign alarm keeps its empty UID, so the backfill writes nothing
	// for it (issue #586).
	if uid := model.AlarmUIDForWrite(a); ex.UID == "" && uid != "" {
		if err := qtx.UpdateTodoAlarmUID(ctx, storage.UpdateTodoAlarmUIDParams{
			Uid: storage.StringToNullable(uid),
			ID:  ex.ID,
		}); err != nil {
			return fmt.Errorf("backfill alarm uid: %w", err)
		}
	}
	if a.Acknowledged != ex.Acknowledged && model.ValidateAcknowledged(a.Acknowledged) {
		if err := qtx.UpdateTodoAlarmAcknowledged(ctx, storage.UpdateTodoAlarmAcknowledgedParams{
			Acknowledged: storage.StringToNullable(a.Acknowledged),
			ID:           ex.ID,
		}); err != nil {
			return fmt.Errorf("update alarm acknowledged: %w", err)
		}
	}
	// X-properties are excluded from content match. Refresh them so a
	// remote X-prop change still lands. nil means the caller has no X-prop
	// knowledge. Keep the stored rows. Only a non-nil slice is authoritative.
	if a.XProperties == nil || model.XPropsContentEqual(a.XProperties, ex.XProperties) {
		return nil
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeTodoAlarm, ex.ID, a.XProperties)
}

// updateTodoAlarmInPlace rewrites a UID-matched alarm's content on its
// stored row. The row ID stays so todo_alarm_state entries survive.
func updateTodoAlarmInPlace(ctx context.Context, qtx *storage.Queries, todoID int64, a model.Alarm, ex model.Alarm) error {
	// A rewrite to a sync-only action disables the alarm, but this code
	// leaves todo_alarm_state alone. The pending and snooze queries
	// filter on the current action, so the state of the alarm comes back
	// when a later pull restores a fireable action (issue #579).
	ack, err := model.PrepareAlarmUpdate(a, ex)
	if err != nil {
		return err
	}
	if err := qtx.UpdateTodoAlarmContentByID(ctx, storage.UpdateTodoAlarmContentByIDParams{
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
		TodoID:        todoID,
	}); err != nil {
		return fmt.Errorf("update alarm content: %w", err)
	}
	if err := qtx.DeleteTodoAlarmAttendeesByAlarmID(ctx, ex.ID); err != nil {
		return fmt.Errorf("delete alarm attendees: %w", err)
	}
	for _, att := range a.Attendees {
		_, err := qtx.CreateTodoAlarmAttendee(ctx, storage.CreateTodoAlarmAttendeeParams{
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
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeTodoAlarm, ex.ID, a.XProperties)
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

// createNewTodoAlarm creates a new todo alarm and its attendees. On a global
// UID collision (for example a server that duplicates a todo with its VALARM
// UIDs), mint a fresh local UID. Do not fail the sync forever.
func createNewTodoAlarm(ctx context.Context, qtx *storage.Queries, todoID int64, a model.Alarm) error {
	if err := model.CheckStorableAlarmAction(a.Action); err != nil {
		return fmt.Errorf("create alarm: %w", err)
	}
	uid := model.AlarmUIDForWrite(a)
	params := storage.CreateTodoAlarmParams{
		TodoID:        todoID,
		Uid:           storage.StringToNullable(uid),
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
	row, err := qtx.CreateTodoAlarm(ctx, params)
	if isUniqueUIDViolation(err) {
		// Retry through the shared rule, like the event service does.
		// The retry must not stamp a minted UID on a preserved foreign
		// alarm (issue #586).
		retry := a
		retry.UID = ""
		params.Uid = storage.StringToNullable(model.AlarmUIDForWrite(retry))
		row, err = qtx.CreateTodoAlarm(ctx, params)
	}
	if err != nil {
		return fmt.Errorf("create alarm: %w", err)
	}
	for _, att := range a.Attendees {
		_, err := qtx.CreateTodoAlarmAttendee(ctx, storage.CreateTodoAlarmAttendeeParams{
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
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeTodoAlarm, row.ID, a.XProperties)
}
