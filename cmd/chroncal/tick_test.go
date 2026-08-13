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

// `chroncal service run` is the primary background sync path, so a sync
// cycle that "succeeded" per its error return but recorded per-phase errors
// on the SyncResult must still fail the tick — otherwise a calendar can be
// half-broken forever with exit 0 and an empty journal. Before the fix the
// loop discarded the result entirely.
func TestRunSyncPassFoldsResultErrors(t *testing.T) {
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
	if err == nil {
		t.Fatal("runSyncPass returned nil despite per-phase errors on the result")
	}
	if !strings.Contains(err.Error(), "Broken") || !strings.Contains(err.Error(), "HTTP 507") {
		t.Errorf("error = %q, want it to name the calendar and the per-phase failure", err)
	}
	if len(fake.synced) != 2 {
		t.Errorf("synced calendars = %v, want both despite calendar 2 failing", fake.synced)
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
