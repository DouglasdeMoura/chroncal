package freebusy

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestCompute_AllDayEveryZone guards issue #875. An all-day event of
// October 28 is busy for the local day of October 28 in every zone. A
// range of the local days October 26 and 27 holds no busy time.
func TestCompute_AllDayEveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		db, q := testutil.NewTestDB(t)
		ctx := context.Background()
		if _, err := event.NewService(db, q).Create(ctx, event.CreateParams{
			CalendarID: 1, Title: "Holiday", AllDay: true,
			StartTime: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("create event: %v", err)
		}
		source := recurrence.NewService(db, q)

		before, err := Compute(ctx, source,
			time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local),
			time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local), nil, nil)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		if len(before.Periods) != 0 {
			t.Fatalf("Oct 26-27 periods = %+v, want none", before.Periods)
		}

		on, err := Compute(ctx, source,
			time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local),
			time.Date(2026, 10, 30, 0, 0, 0, 0, time.Local), nil, nil)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		wantStart := time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local)
		wantEnd := time.Date(2026, 10, 29, 0, 0, 0, 0, time.Local)
		if len(on.Periods) != 1 || !on.Periods[0].Start.Equal(wantStart) || !on.Periods[0].End.Equal(wantEnd) {
			t.Fatalf("Oct 27-29 periods = %+v, want one period [%s, %s)", on.Periods, wantStart, wantEnd)
		}
	})
}
