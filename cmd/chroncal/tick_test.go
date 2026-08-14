package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// fakeTickSyncer drives runSyncPass without a CalDAV server.
type fakeTickSyncer struct {
	statuses []syncPkg.SyncStatus
	results  map[int64]*syncPkg.SyncResult
	errs     map[int64]error
	synced   []int64
}

func (f *fakeTickSyncer) Status(ctx context.Context) ([]syncPkg.SyncStatus, error) {
	return f.statuses, nil
}

func (f *fakeTickSyncer) SyncCalendar(ctx context.Context, calendarID int64, strategy syncPkg.ConflictStrategy) (*syncPkg.SyncResult, error) {
	f.synced = append(f.synced, calendarID)
	return f.results[calendarID], f.errs[calendarID]
}

// A per-item sync error recorded on SyncResult.Errors (SyncCalendar itself
// returned nil) must NOT fail the tick. The engine already logs each such
// error to the stderr logger. That logger reaches the journal on `service
// run`. The error stays visible. A single permanently-stuck remote item
// should not flip `service run`'s exit code to non-zero on every cycle
// forever. That would drown any exit-code monitor in noise it cannot act on.
// Only hard failures (below) fail the tick.
func TestRunSyncPassPartialErrorsDoNotFailTheTick(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	fake := &fakeTickSyncer{
		statuses: []syncPkg.SyncStatus{
			{CalendarID: 1, CalendarName: "Clean"},
			{CalendarID: 2, CalendarName: "Broken"},
		},
		results: map[int64]*syncPkg.SyncResult{
			1: {CalendarID: 1, Pushed: 1},
			2: {CalendarID: 2, Errors: []error{errors.New("push item x: HTTP 507")}},
		},
	}

	err := runSyncPass(context.Background(), fake, now, 15*time.Minute, syncPkg.ConflictServerWins)
	if err != nil {
		t.Errorf("runSyncPass = %q, want nil: a per-item result error is logged by the engine, not a tick failure", err)
	}
	if len(fake.synced) != 2 {
		t.Errorf("synced calendars = %v, want both attempted", fake.synced)
	}
}

// A hard SyncCalendar error (auth, network, DB — the whole calendar failed to
// sync) fails the tick, naming the calendar. Every other due calendar is
// still attempted.
func TestRunSyncPassHardErrorFailsTheTick(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	fake := &fakeTickSyncer{
		statuses: []syncPkg.SyncStatus{
			{CalendarID: 1, CalendarName: "Clean"},
			{CalendarID: 2, CalendarName: "Broken"},
		},
		results: map[int64]*syncPkg.SyncResult{1: {CalendarID: 1, Pushed: 1}},
		errs:    map[int64]error{2: errors.New("credential store: keyring locked")},
	}

	err := runSyncPass(context.Background(), fake, now, 15*time.Minute, syncPkg.ConflictServerWins)
	if err == nil {
		t.Fatal("runSyncPass returned nil despite a hard SyncCalendar failure")
	}
	if !strings.Contains(err.Error(), "Broken") || !strings.Contains(err.Error(), "keyring locked") {
		t.Errorf("error = %q, want it to name the calendar and the hard failure", err)
	}
	if len(fake.synced) != 2 {
		t.Errorf("synced calendars = %v, want both attempted despite calendar 2 failing", fake.synced)
	}
}

// A cycle where every due calendar syncs cleanly must stay silent and nil,
// and calendars not yet due must be skipped.
func TestRunSyncPassCleanCycleAndDueFilter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	fake := &fakeTickSyncer{
		statuses: []syncPkg.SyncStatus{
			{CalendarID: 1, CalendarName: "Due"},
			{CalendarID: 2, CalendarName: "Fresh", LastSyncAttemptedAt: "2026-04-03T11:55:00Z"},
		},
		results: map[int64]*syncPkg.SyncResult{1: {CalendarID: 1}},
	}

	if err := runSyncPass(context.Background(), fake, now, 15*time.Minute, syncPkg.ConflictServerWins); err != nil {
		t.Fatalf("runSyncPass clean cycle: %v", err)
	}
	if len(fake.synced) != 1 || fake.synced[0] != 1 {
		t.Errorf("synced calendars = %v, want only the due calendar 1", fake.synced)
	}
}

func TestSyncDue(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		lastAttempt string
		interval    time.Duration
		want        bool
	}{
		{name: "disabled interval", lastAttempt: "", interval: 0, want: false},
		{name: "never synced", lastAttempt: "", interval: 15 * time.Minute, want: true},
		{name: "due", lastAttempt: "2026-04-03T11:30:00Z", interval: 15 * time.Minute, want: true},
		{name: "not due", lastAttempt: "2026-04-03T11:50:00Z", interval: 15 * time.Minute, want: false},
		{name: "invalid timestamp treated as due", lastAttempt: "not-a-time", interval: 15 * time.Minute, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := syncDue(now, tt.lastAttempt, tt.interval); got != tt.want {
				t.Fatalf("syncDue(%q, %v) = %v, want %v", tt.lastAttempt, tt.interval, got, tt.want)
			}
		})
	}
}
