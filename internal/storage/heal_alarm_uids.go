package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// backfillAlarmUIDs assigns random UUIDs to alarms that have empty UIDs.
// This runs once after upgrade from pre-UID schema.
//
// An alarm the engine cannot fire keeps its empty UID. Import preserves
// such an alarm from another client (issue #579), and a local UID would
// travel back to the server on the next push. Some servers and clients
// read a new UID as a different alarm. The row still matches on its
// content during a pull, and it holds no alarm state, because it never
// fires (issue #586).
func backfillAlarmUIDs(conn *sql.DB, q *Queries) error {
	ctx := context.Background()

	storedAlarms, err := q.ListAlarmsWithEmptyUID(ctx)
	if err != nil {
		return fmt.Errorf("list alarms with empty uid: %w", err)
	}
	storedTodoAlarms, err := q.ListTodoAlarmsWithEmptyUID(ctx)
	if err != nil {
		return fmt.Errorf("list todo alarms with empty uid: %w", err)
	}
	// model.AlarmUIDForWrite owns the rule. It yields an empty value for
	// an action the engine cannot fire, and such a row keeps its absent
	// UID (issue #586).
	type uidFix struct {
		id  int64
		uid string
	}
	var alarms, todoAlarms []uidFix
	for _, a := range storedAlarms {
		if uid := model.AlarmUIDForWrite(model.Alarm{Action: a.Action}); uid != "" {
			alarms = append(alarms, uidFix{id: a.ID, uid: uid})
		}
	}
	for _, a := range storedTodoAlarms {
		if uid := model.AlarmUIDForWrite(model.Alarm{Action: a.Action}); uid != "" {
			todoAlarms = append(todoAlarms, uidFix{id: a.ID, uid: uid})
		}
	}
	if len(alarms) == 0 && len(todoAlarms) == 0 {
		return nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	qtx := q.WithTx(tx)
	for _, a := range alarms {
		if err := qtx.UpdateAlarmUID(ctx, UpdateAlarmUIDParams{
			Uid: StringToNullable(a.uid),
			ID:  a.id,
		}); err != nil {
			return err
		}
	}
	for _, a := range todoAlarms {
		if err := qtx.UpdateTodoAlarmUID(ctx, UpdateTodoAlarmUIDParams{
			Uid: StringToNullable(a.uid),
			ID:  a.id,
		}); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit alarm UID backfill: %w", err)
	}
	return nil
}
