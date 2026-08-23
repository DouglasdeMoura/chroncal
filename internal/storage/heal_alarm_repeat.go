package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// normalizeAlarmRepeatPairs heals alarm rows written before the parsers
// validated REPEAT and DURATION (issues #580 and #581). It clamps the
// repeat count, clears an interval that fails model.ValidAlarmDuration,
// and clears an unpaired value. Without this repair, export and the
// next pull disagree with the stored row. The mismatch deletes and
// recreates the alarm row, and the cascade discards the alarm state.
//
// The pass does not delete a row whose trigger_value fails
// model.ParseableAlarmTrigger. Such a row is inert: the fire path
// cannot read it and export omits its VALARM. A delete would cascade
// away the alarm state. It would also destroy an RFC-valid trigger
// from another client that only this build cannot represent. The keep
// decision protects only the local row and its state. The next push of
// that resource still omits the VALARM, so the server copy can lose
// the alarm through an ordinary edit.
//
// The writes for each table run in one transaction, like
// backfillAlarmUIDs, and a failure is fatal to Open, like every startup
// pass here. The function runs on every startup. It changes nothing
// when all rows are healthy.
func normalizeAlarmRepeatPairs(conn *sql.DB) error {
	ctx := context.Background()
	for _, table := range []string{"event_alarms", "todo_alarms"} {
		type fix struct {
			id       int64
			repeat   int
			duration string
		}
		var fixes []fix
		err := func() error {
			// Read the action so the heal can skip a preserved sync-only
			// alarm. The repeat and the duration of such an alarm are
			// round-trip data, not fire-path data, so a repair here would
			// rewrite the VALARM of another client (issue #579).
			rows, err := conn.QueryContext(ctx,
				`SELECT id, action, repeat, COALESCE(duration, '') FROM `+table+
					` WHERE repeat > 0 OR COALESCE(duration, '') != ''`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var a model.Alarm
				if err := rows.Scan(&id, &a.Action, &a.Repeat, &a.Duration); err != nil {
					return err
				}
				if !model.FireableAlarmAction(a.Action) {
					continue
				}
				healed := a
				healed.Repeat = min(healed.Repeat, model.MaxAlarmRepeat)
				if !model.ValidAlarmDuration(healed.Duration) {
					healed.Duration = ""
				}
				if !healed.RepeatPaired() {
					healed.Repeat, healed.Duration = 0, ""
				}
				if healed.Repeat != a.Repeat || healed.Duration != a.Duration {
					fixes = append(fixes, fix{id, healed.Repeat, healed.Duration})
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return fmt.Errorf("scan %s: %w", table, err)
		}
		if len(fixes) == 0 {
			continue
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("heal %s: %w", table, err)
		}
		for _, f := range fixes {
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+table+` SET repeat = ?, duration = NULLIF(?, '') WHERE id = ?`,
				f.repeat, f.duration, f.id); err != nil {
				tx.Rollback()
				return fmt.Errorf("heal %s row %d: %w", table, f.id, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("heal %s: %w", table, err)
		}
	}
	return nil
}
