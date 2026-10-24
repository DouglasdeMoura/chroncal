package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pressly/goose/v3"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// migration064Version follows migration 063, which makes an alarm UID
// unique in its item. The heal compares alarms with the Go content rule
// (model.Alarm.ContentEqual), so it is a Go migration.
const migration064Version = 64

// newTwinAlarmUIDMigration gives back the UID of the file to the alarms
// that the store minted before migration 063 (issue #922).
//
// Before migration 063, an alarm UID was unique in its table. Two copies of
// one item, with one iCal UID in two calendars, could not hold the same
// alarm UID. The store kept a minted UID for the alarm of the later copy.
// The earlier copy still holds the UID of the file. For a synced item, the
// server also holds it.
//
// For each item with a twin (the same iCal UID and recurrence ID on another
// row), the heal pairs each alarm with an earlier alarm of a twin. The pair
// needs equal content, an earlier row ID, and a different UID that no alarm
// of the item has. The later alarm then takes the UID of the earlier alarm.
// The row stays, so its alarm state stays.
//
// Todos need no heal: the UID of a todo is unique in its table, so a todo
// has no twin (issue #911).
//
// The migration runs once. A startup heal would also change two different
// UIDs that a client wrote after the migration.
func newTwinAlarmUIDMigration() *goose.Migration {
	return goose.NewGoMigration(
		migration064Version,
		&goose.GoFunc{RunTx: healTwinAlarmUIDsTx},
		// The Down direction restores nothing. The minted UIDs had no
		// meaning, and the down migration of 063 clears a UID on a
		// second item.
		&goose.GoFunc{RunTx: func(context.Context, *sql.Tx) error { return nil }},
	)
}

// twinAlarmTables names the tables of one item kind for the heal.
type twinAlarmTables struct {
	items, alarms, owner, attendees string
}

func healTwinAlarmUIDsTx(ctx context.Context, tx *sql.Tx) error {
	t := twinAlarmTables{items: "events", alarms: "event_alarms", owner: "event_id", attendees: "event_alarm_attendees"}
	if err := healTwinAlarmUIDs(ctx, tx, t); err != nil {
		return fmt.Errorf("heal the alarm UIDs of %s: %w", t.items, err)
	}
	return nil
}

func healTwinAlarmUIDs(ctx context.Context, tx *sql.Tx, t twinAlarmTables) error {
	groups, err := twinGroups(ctx, tx, t.items)
	if err != nil {
		return err
	}
	for _, ids := range groups {
		alarms, err := twinGroupAlarms(ctx, tx, t, ids)
		if err != nil {
			return err
		}
		for _, fix := range twinAlarmFixes(ids, alarms) {
			if _, err := tx.ExecContext(ctx, `UPDATE `+t.alarms+` SET uid = ? WHERE id = ?`, fix.uid, fix.id); err != nil {
				return fmt.Errorf("set alarm uid: %w", err)
			}
		}
	}
	return nil
}

// twinGroups returns the row IDs of each set of items with one iCal UID
// and one recurrence ID, in the order of the row IDs.
func twinGroups(ctx context.Context, tx *sql.Tx, items string) ([][]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, uid || char(0) || COALESCE(recurrence_id, '') FROM `+items+` AS i
		WHERE EXISTS (SELECT 1 FROM `+items+` AS j
			WHERE j.uid = i.uid AND COALESCE(j.recurrence_id, '') = COALESCE(i.recurrence_id, '') AND j.id <> i.id)
		ORDER BY uid, COALESCE(recurrence_id, ''), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups [][]int64
	last := ""
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		if len(groups) == 0 || key != last {
			groups = append(groups, nil)
			last = key
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], id)
	}
	return groups, rows.Err()
}

// twinAlarm is an alarm of an item of a twin group.
type twinAlarm struct {
	owner int64
	alarm model.Alarm
}

// twinGroupAlarms returns the alarms of the items ids, in the order of the
// alarm row IDs, with their attendees.
func twinGroupAlarms(ctx context.Context, tx *sql.Tx, t twinAlarmTables, ids []int64) ([]twinAlarm, error) {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, `+t.owner+`, action, trigger_value, description, repeat, duration, related,
			summary, uid, attach_uri, attach_fmttype, attach_binary
		FROM `+t.alarms+` WHERE `+t.owner+` IN (`+marks+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	var out []twinAlarm
	byID := map[int64]int{}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var a twinAlarm
			var description, duration, summary, uid, attachURI, attachType *string
			var repeat int64
			if err := rows.Scan(&a.alarm.ID, &a.owner, &a.alarm.Action, &a.alarm.TriggerValue, &description,
				&repeat, &duration, &a.alarm.Related, &summary, &uid, &attachURI, &attachType, &a.alarm.AttachBinary); err != nil {
				return err
			}
			a.alarm.Description, a.alarm.Duration, a.alarm.Summary = NullableToString(description), NullableToString(duration), NullableToString(summary)
			a.alarm.UID, a.alarm.AttachURI, a.alarm.AttachFmtType = NullableToString(uid), NullableToString(attachURI), NullableToString(attachType)
			a.alarm.Repeat = int(repeat)
			byID[a.alarm.ID] = len(out)
			out = append(out, a)
		}
		return rows.Err()
	}()
	if err != nil || len(out) == 0 {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT alarm_id, email, name FROM `+t.attendees+`
		WHERE alarm_id IN (SELECT id FROM `+t.alarms+` WHERE `+t.owner+` IN (`+marks+`)) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var alarmID int64
		var att model.AlarmAttendee
		var name *string
		if err := rows.Scan(&alarmID, &att.Email, &name); err != nil {
			return nil, err
		}
		att.Name = NullableToString(name)
		if i, ok := byID[alarmID]; ok {
			out[i].alarm.Attendees = append(out[i].alarm.Attendees, att)
		}
	}
	return out, rows.Err()
}

// twinAlarmFix is one UID that the heal writes.
type twinAlarmFix struct {
	id  int64
	uid string
}

// twinAlarmFixes returns the UIDs that the alarms of the twin group take.
// alarms is in the order of the row IDs. An alarm takes the UID of the
// first earlier alarm of a twin with equal content and a different UID.
// No alarm of its item may have that UID, and one earlier alarm gives its
// UID to one alarm of each item at most.
func twinAlarmFixes(ids []int64, alarms []twinAlarm) []twinAlarmFix {
	held := make(map[int64]map[string]bool, len(ids))
	for _, id := range ids {
		held[id] = map[string]bool{}
	}
	for _, a := range alarms {
		if a.alarm.UID != "" {
			held[a.owner][a.alarm.UID] = true
		}
	}
	type use struct {
		source int
		owner  int64
	}
	used := map[use]bool{}
	var fixes []twinAlarmFix
	for i, a := range alarms {
		for j := range i {
			b := alarms[j]
			if b.owner == a.owner || b.alarm.UID == "" || b.alarm.UID == a.alarm.UID ||
				held[a.owner][b.alarm.UID] || used[use{j, a.owner}] || !a.alarm.ContentEqual(b.alarm) {
				continue
			}
			used[use{j, a.owner}] = true
			delete(held[a.owner], a.alarm.UID)
			held[a.owner][b.alarm.UID] = true
			alarms[i].alarm.UID = b.alarm.UID
			fixes = append(fixes, twinAlarmFix{id: a.alarm.ID, uid: b.alarm.UID})
			break
		}
	}
	return fixes
}
