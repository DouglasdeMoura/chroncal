package tui

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestRunEventDeleteScope_LegacyOverrideNamesMasterCalendar: a scope delete
// of a legacy override writes the calendar of the master. The message names
// that calendar, so the push sends the changed series.
func TestRunEventDeleteScope_LegacyOverrideNamesMasterCalendar(t *testing.T) {
	_, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cals)
	masterCal := cals[0].ID
	res, err := a.DB.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Legacy')`)
	require.NoError(t, err)
	legacyCal, err := res.LastInsertId()
	require.NoError(t, err)

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "tui-legacy", CalendarID: masterCal, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	require.NoError(t, err)
	legacy, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: legacyCal, Title: "Legacy",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	require.NoError(t, err)

	msg := runEventDeleteScope(ctx, a, legacy, 0)
	require.NoError(t, msg.err)
	require.Equal(t, masterCal, msg.calendarID, "the message names the calendar of the master")
}

// TestRunEventDelete_LegacyOverrideNamesMasterCalendar: a plain delete of
// a legacy override writes the EXDATE on its master. The message names the
// calendar of the master.
func TestRunEventDelete_LegacyOverrideNamesMasterCalendar(t *testing.T) {
	_, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	masterCal := cals[0].ID
	res, err := a.DB.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Legacy')`)
	require.NoError(t, err)
	legacyCal, err := res.LastInsertId()
	require.NoError(t, err)
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "tui-plain-legacy", CalendarID: masterCal, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	require.NoError(t, err)
	legacy, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: legacyCal, Title: "Legacy",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	require.NoError(t, err)

	msg := runEventDelete(ctx, a, legacy)
	require.NoError(t, msg.err)
	require.Equal(t, masterCal, msg.calendarID, "the message names the calendar of the master")
}
