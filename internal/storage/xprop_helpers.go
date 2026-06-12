package storage

import (
	"context"
	"fmt"
	"log"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// Alarm X-property owner types in the polymorphic x_properties table.
const (
	OwnerTypeEventAlarm = "event_alarm"
	OwnerTypeTodoAlarm  = "todo_alarm"
)

// AttachAlarmXProperties batch-loads X-properties for the given alarms and
// attaches them by alarm ID. Best-effort: a load failure leaves XProperties
// nil rather than failing the alarm fetch.
func AttachAlarmXProperties(ctx context.Context, q *Queries, ownerType string, alarms []model.Alarm) {
	if len(alarms) == 0 {
		return
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
		log.Printf("AttachAlarmXProperties: failed to load x-properties for %d alarms: %v", len(ids), err)
		return
	}
	if len(rows) == 0 {
		return
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
