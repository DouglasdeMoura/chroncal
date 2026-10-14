package uidmove

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

var (
	errInstance = errors.New("instance move")
	errTarget   = errors.New("target has uid")
)

// fakeRows returns Rows with masters in the calendars of masters and
// collisions rows in each target calendar.
func fakeRows(masters map[int64]bool, collisions int64) Rows {
	return Rows{
		UID:             "u1",
		ErrInstanceMove: errInstance,
		ErrTargetHasUID: errTarget,
		HasMaster: func(_ context.Context, calendarID int64) (bool, error) {
			return masters[calendarID], nil
		},
		CountCollisions: func(context.Context, int64, int64) (int64, error) {
			return collisions, nil
		},
	}
}

func TestCheckOverrideMove(t *testing.T) {
	t.Parallel()
	const a, b = int64(1), int64(2)
	tests := []struct {
		name    string
		masters map[int64]bool
		recID   string
		to      int64
		want    error
	}{
		{"master row moves", map[int64]bool{a: true}, "", b, nil},
		{"override keeps its calendar", map[int64]bool{a: true}, "r", a, nil},
		{"override leaves its master", map[int64]bool{a: true}, "r", b, errInstance},
		// The source calendar holds the master of the override. A copy of
		// the series in the target calendar does not make it a legacy row.
		{"override leaves its master for a copy", map[int64]bool{a: true, b: true}, "r", b, errInstance},
		{"legacy override goes back to its master", map[int64]bool{b: true}, "r", b, nil},
		{"legacy override goes to no master", map[int64]bool{}, "r", b, errInstance},
		{"override update names no calendar", map[int64]bool{a: true}, "r", 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := fakeRows(tc.masters, 0).CheckOverrideMove(context.Background(), tc.recID, a, tc.to)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("CheckOverrideMove = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, q := testutil.NewTestDB(t)
	cal, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Work"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	r := fakeRows(nil, 1)
	r.Q = q
	if err := r.CheckTarget(ctx, cal.ID, cal.ID); err != nil {
		t.Fatalf("CheckTarget in one calendar = %v, want nil", err)
	}
	err = r.CheckTarget(ctx, 99, cal.ID)
	if !errors.Is(err, errTarget) {
		t.Fatalf("CheckTarget with a collision = %v, want the target error", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"Work"`) || !strings.Contains(msg, "u1") || !strings.Contains(msg, "purge or restore it first") {
		t.Fatalf("error = %q, want the calendar, the UID, and the guidance", msg)
	}

	// A domain whose schema forbids a collision gives no count.
	r.CountCollisions = nil
	if err := r.CheckTarget(ctx, 99, cal.ID); err != nil {
		t.Fatalf("CheckTarget with no count = %v, want nil", err)
	}
}
