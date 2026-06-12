package storage

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// Alarm X-property owner types in the polymorphic x_properties table.
const (
	OwnerTypeEventAlarm = "event_alarm"
	OwnerTypeTodoAlarm  = "todo_alarm"
)

// AttachAlarmXProperties batch-loads X-properties for the given alarms and
// attaches them by alarm ID. A load failure is returned, not swallowed: the
// result feeds export and sync pushes, and silently-nil XProperties would
// rewrite the server copy without them — turning a transient local read
// error into permanent remote data loss.
func AttachAlarmXProperties(ctx context.Context, q *Queries, ownerType string, alarms []model.Alarm) error {
	if len(alarms) == 0 {
		return nil
	}
	ids := make([]int64, len(alarms))
	for i, a := range alarms {
		ids[i] = a.ID
	}
	rows, err := q.ListXPropertiesByOwnerIDs(ctx, ListXPropertiesByOwnerIDsParams{
		OwnerType: ownerType,
		OwnerIds:  ids,
	})
	if err != nil {
		return fmt.Errorf("load alarm x-properties: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	byOwner := make(map[int64][]model.XProperty)
	for _, r := range rows {
		byOwner[r.OwnerID] = append(byOwner[r.OwnerID], model.XProperty{
			ID: r.ID, OwnerType: r.OwnerType, OwnerID: r.OwnerID,
			Name: r.Name, Value: r.Value, Params: r.Params,
		})
	}
	for i := range alarms {
		alarms[i].XProperties = byOwner[alarms[i].ID]
	}
	return nil
}

// ReplaceAlarmXProperties rewrites an alarm's X-properties. Call inside the
// caller's transaction by passing the transactional Queries.
func ReplaceAlarmXProperties(ctx context.Context, qtx *Queries, ownerType string, alarmID int64, xprops []model.XProperty) error {
	if err := qtx.DeleteXPropertiesByOwner(ctx, DeleteXPropertiesByOwnerParams{
		OwnerType: ownerType, OwnerID: alarmID,
	}); err != nil {
		return fmt.Errorf("delete alarm x-properties: %w", err)
	}
	for _, xp := range xprops {
		params := xp.Params
		if params == "" {
			params = "{}"
		}
		if err := qtx.InsertXProperty(ctx, InsertXPropertyParams{
			OwnerType: ownerType, OwnerID: alarmID,
			Name: xp.Name, Value: xp.Value, Params: params,
		}); err != nil {
			return fmt.Errorf("insert alarm x-property: %w", err)
		}
	}
	return nil
}
