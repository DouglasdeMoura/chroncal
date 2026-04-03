package sync

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// mockCredStore implements auth.CredentialStore for testing.
type mockCredStore struct {
	creds map[int64]auth.Credential
}

func (m *mockCredStore) Get(accountID int64) (auth.Credential, error) {
	c, ok := m.creds[accountID]
	if !ok {
		return auth.Credential{}, nil
	}
	return c, nil
}

func (m *mockCredStore) Set(cred auth.Credential) error { return nil }
func (m *mockCredStore) Delete(accountID int64) error   { return nil }

func newTestService(t *testing.T) (*Service, *storage.Queries) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	credStore := &mockCredStore{creds: make(map[int64]auth.Credential)}
	svc := NewService(db, q, credStore, nil)
	return svc, q
}

func TestService_StatusEmpty(t *testing.T) {
	svc, _ := newTestService(t)
	statuses, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses, got %d", len(statuses))
	}
}

func TestService_ListConflictsEmpty(t *testing.T) {
	svc, _ := newTestService(t)
	conflicts, err := svc.ListConflicts(context.Background())
	if err != nil {
		t.Fatalf("ListConflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(conflicts))
	}
}

func TestService_ResolveConflict_InvalidPick(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	// Create an account and a linked calendar so we can create a conflict
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Use the seeded calendar
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID
	_ = account

	// Create a conflict
	err = q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "test-uid",
		LocalIcal:  "BEGIN:VCALENDAR\nEND:VCALENDAR",
		ServerIcal: "BEGIN:VCALENDAR\nEND:VCALENDAR",
		ServerEtag: "etag-123",
	})
	if err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	conflicts, _ := q.ListSyncConflicts(ctx)
	if len(conflicts) == 0 {
		t.Fatal("expected at least 1 conflict")
	}

	// Resolve with invalid pick
	err = svc.ResolveConflict(ctx, conflicts[0].ID, "invalid")
	if err == nil {
		t.Error("expected error for invalid pick value")
	}
}

func TestService_ResolveConflict_Server(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "resolve-server-uid",
		LocalIcal:  "local",
		ServerIcal: "server",
		ServerEtag: "etag-456",
	})
	if err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	conflicts, _ := q.ListSyncConflicts(ctx)
	err = svc.ResolveConflict(ctx, conflicts[0].ID, "server")
	if err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}

	// Conflict should be deleted
	remaining, _ := q.ListSyncConflicts(ctx)
	if len(remaining) != 0 {
		t.Errorf("expected 0 conflicts after resolve, got %d", len(remaining))
	}
}

func TestService_ResetCalendar(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	// Create some sync state
	_ = q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          "reset-test-uid",
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/event.ics",
		Etag:         "etag-789",
		Dirty:        1,
		SyncStrategy: "sync-token",
	})
	_ = q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calID,
		Uid:        "reset-tombstone",
		RemoteUrl:  "https://example.com/cal/old.ics",
	})
	_ = q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "reset-conflict",
		LocalIcal:  "local",
		ServerIcal: "server",
		ServerEtag: "etag",
	})

	// Reset
	err := svc.ResetCalendar(ctx, calID)
	if err != nil {
		t.Fatalf("ResetCalendar: %v", err)
	}

	// All sync state should be gone
	resources, _ := q.ListSyncResourcesByCalendar(ctx, calID)
	if len(resources) != 0 {
		t.Errorf("expected 0 sync resources, got %d", len(resources))
	}
	tombstones, _ := q.ListTombstonesByCalendar(ctx, calID)
	if len(tombstones) != 0 {
		t.Errorf("expected 0 tombstones, got %d", len(tombstones))
	}
	conflicts, _ := q.ListSyncConflictsByCalendar(ctx, calID)
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(conflicts))
	}
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"2026-04-03T12:00:00Z", time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)},
		{"2026-04-03 12:00:00", time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"invalid", time.Time{}},
	}
	for _, tt := range tests {
		got := parseTime(tt.input)
		if !got.Equal(tt.want) {
			t.Errorf("parseTime(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
