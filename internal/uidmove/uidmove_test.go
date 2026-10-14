package uidmove

import (
	"context"
	"errors"
	"slices"
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
		CountCollisions: func(context.Context, int64, int64) (int64, error) {
			return collisions, nil
		},
		Masters: func(context.Context) ([]Master, error) {
			var out []Master
			for id, ok := range masters {
				if ok {
					out = append(out, Master{ID: id * 10, CalendarID: id})
				}
			}
			slices.SortFunc(out, func(a, b Master) int { return int(a.ID - b.ID) })
			return out, nil
		},
	}
}

func TestCheckOverrideRule(t *testing.T) {
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
			rows := fakeRows(tc.masters, 0)
			err := rows.Check(context.Background(), tc.recID, a, tc.to)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("Check = %v, want %v", err, tc.want)
			}
			// The rule for a caller that holds the masters gives the same
			// answer.
			masters, _ := rows.Masters(context.Background())
			err = rows.CheckOverrideMove(tc.recID, a, tc.to, masters)
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

// TestCheckOverrideMoveNamesTheMasters checks the masters that the refusal
// carries: the master in the calendar of the row, or else every master.
func TestCheckOverrideMoveNamesTheMasters(t *testing.T) {
	t.Parallel()
	const a, b, c = int64(1), int64(2), int64(3)
	for _, tc := range []struct {
		name    string
		masters map[int64]bool
		from    int64
		want    []Master
	}{
		{"own master", map[int64]bool{a: true, c: true}, a, []Master{{ID: 10, CalendarID: a}}},
		{"legacy row with two masters", map[int64]bool{a: true, c: true}, b, []Master{{ID: 10, CalendarID: a}, {ID: 30, CalendarID: c}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := fakeRows(tc.masters, 0).Check(context.Background(), "r", tc.from, 99)
			var moveErr *OccurrenceMoveError
			if !errors.As(err, &moveErr) || !errors.Is(err, errInstance) {
				t.Fatalf("Check = %v, want an OccurrenceMoveError that wraps the domain error", err)
			}
			if !slices.Equal(moveErr.Masters, tc.want) {
				t.Fatalf("masters = %+v, want %+v", moveErr.Masters, tc.want)
			}
		})
	}
}

// TestCheckPropagatesMastersError fails the read of the masters. Check
// must return that error, not a refusal or a pass.
func TestCheckPropagatesMastersError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	rows := fakeRows(nil, 0)
	rows.Masters = func(context.Context) ([]Master, error) { return nil, boom }
	if err := rows.Check(context.Background(), "r", 1, 2); !errors.Is(err, boom) || errors.Is(err, errInstance) {
		t.Fatalf("Check = %v, want the masters error", err)
	}
	// A master row needs no master read.
	if err := rows.Check(context.Background(), "", 1, 2); err != nil {
		t.Fatalf("Check of a master row = %v, want nil", err)
	}
}
