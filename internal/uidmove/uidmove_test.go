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
// collisions rows in the trash of each target calendar.
func fakeRows(masters map[int64]bool, collisions int64) Rows {
	return Rows{
		UID:             "u1",
		ErrInstanceMove: errInstance,
		ErrTargetHasUID: errTarget,
		CountCollisions: func(context.Context, int64, int64) (int64, int64, error) {
			return 0, collisions, nil
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
	for _, tc := range []struct {
		name          string
		live, trashed int64
		want, notWant string
	}{
		{"copy in the trash", 0, 1, "holds UID u1 in the trash; purge or restore the trashed copy first", "live copy"},
		{"live copy", 1, 0, "holds a live copy of UID u1; delete or move that copy first", "trash"},
		{"live copy and trashed copy", 1, 1, "holds a live copy of UID u1 and a copy in the trash", ""},
	} {
		r.CountCollisions = func(context.Context, int64, int64) (int64, int64, error) { return tc.live, tc.trashed, nil }
		err := r.CheckTarget(ctx, 99, cal.ID)
		if !errors.Is(err, errTarget) {
			t.Fatalf("%s: CheckTarget = %v, want the target error", tc.name, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, `"Work"`) || !strings.Contains(msg, tc.want) || (tc.notWant != "" && strings.Contains(msg, tc.notWant)) {
			t.Fatalf("%s: error = %q, want %q", tc.name, msg, tc.want)
		}
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

// TestLegacyOverrideCountsTrashedMaster checks the definition of a legacy
// override: an override row in a calendar that holds no master row of the
// UID, live or in the trash. A calendar with a master in the trash still
// owns its overrides, so they cannot move away. A move target needs a
// live master. A refusal names live masters only.
func TestLegacyOverrideCountsTrashedMaster(t *testing.T) {
	t.Parallel()
	const a, b, c = int64(1), int64(2), int64(3)
	rows := fakeRows(nil, 0)
	rows.Masters = func(context.Context) ([]Master, error) {
		return []Master{{ID: 10, CalendarID: a, Trashed: true}, {ID: 20, CalendarID: b}}, nil
	}
	ctx := context.Background()

	// The override in A belongs to the trashed series of A.
	err := rows.Check(ctx, "r", a, b)
	var moveErr *OccurrenceMoveError
	if !errors.As(err, &moveErr) {
		t.Fatalf("move out of a calendar with a trashed master = %v, want a refusal", err)
	}
	if len(moveErr.Masters) != 1 || moveErr.Masters[0].ID != 20 {
		t.Fatalf("masters of the refusal = %+v, want the live master 20 only", moveErr.Masters)
	}
	// A legacy override in C cannot go to A, whose master is in the trash.
	if err := rows.Check(ctx, "r", c, a); !errors.Is(err, errInstance) {
		t.Fatalf("move to a calendar with a trashed master = %v, want a refusal", err)
	}
	// It can go to B, whose master is live.
	if err := rows.Check(ctx, "r", c, b); err != nil {
		t.Fatalf("move to a calendar with a live master = %v, want nil", err)
	}
}

// TestSingleMasterRows checks the Rows of a domain with one master row per
// UID: no master row gives no master, and MoveRows runs the steps in order
// and names the step that fails.
func TestSingleMasterRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var found bool
	var ran []string
	boom := errors.New("boom")
	rows := SingleMasterRows(nil, "u1", errInstance,
		func(context.Context) (Master, bool, error) { return Master{ID: 7, CalendarID: 1}, found, nil },
		MoveStep{Name: "items", Move: func(context.Context, int64, int64) error { ran = append(ran, "items"); return nil }},
		MoveStep{Name: "log", Move: func(context.Context, int64, int64) error { ran = append(ran, "log"); return boom }},
	)
	if m, err := rows.Masters(ctx); err != nil || len(m) != 0 {
		t.Fatalf("Masters with no master row = %+v, %v; want none", m, err)
	}
	found = true
	if m, err := rows.Masters(ctx); err != nil || len(m) != 1 || m[0].ID != 7 {
		t.Fatalf("Masters = %+v, %v; want master 7", m, err)
	}
	err := rows.MoveRows(ctx, 1, 2, false)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "move log") {
		t.Fatalf("MoveRows = %v, want the failed step named", err)
	}
	if strings.Join(ran, ",") != "items,log" {
		t.Fatalf("steps = %v, want items then log", ran)
	}
	if rows.CountCollisions != nil {
		t.Fatal("CountCollisions is set, want nil")
	}
}

// TestUpdateKeepsCalendarForZeroTarget runs Rows.Update with target
// calendar 0, which keeps the calendar of the row. The write must get the
// calendar of the row, not 0.
func TestUpdateKeepsCalendarForZeroTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, q := testutil.NewTestDB(t)
	cal, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Work"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	r := fakeRows(nil, 0)
	r.Q = q
	r.MoveRows = func(context.Context, int64, int64, bool) error { return nil }
	var got int64 = -1
	if err := r.Update(ctx, "VEVENT", "event", "", cal.ID, 0, func(_ context.Context, calendarID int64) error {
		got = calendarID
		return nil
	}); err != nil {
		t.Fatalf("Update = %v", err)
	}
	if got != cal.ID {
		t.Fatalf("write got calendar %d, want the calendar of the row %d", got, cal.ID)
	}
}

func TestLegacyOwner(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		masters       []Master
		want          int64
		ok, ambiguous bool
	}{
		{name: "no master"},
		{name: "one live", masters: []Master{{CalendarID: 1}}, want: 1, ok: true},
		{name: "one live, one trashed", masters: []Master{{CalendarID: 1}, {CalendarID: 2, Trashed: true}}, want: 1, ok: true},
		{name: "one trashed", masters: []Master{{CalendarID: 2, Trashed: true}}, want: 2, ok: true},
		{name: "two live", masters: []Master{{CalendarID: 1}, {CalendarID: 2}}, ambiguous: true},
		{name: "two trashed", masters: []Master{{CalendarID: 1, Trashed: true}, {CalendarID: 2, Trashed: true}}, ambiguous: true},
	} {
		got, ok, ambiguous := LegacyOwner(tc.masters)
		if got != tc.want || ok != tc.ok || ambiguous != tc.ambiguous {
			t.Errorf("%s: LegacyOwner = (%d, %v, %v), want (%d, %v, %v)", tc.name, got, ok, ambiguous, tc.want, tc.ok, tc.ambiguous)
		}
	}
}
