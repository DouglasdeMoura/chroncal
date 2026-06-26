package storage

import (
	"context"
	"testing"
)

func TestMarkResourceDirty_NoopWhenNoSyncResource(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Should be a no-op when no sync_resources row exists (no account linked)
	err = MarkResourceDirty(context.Background(), db, 1, "non-existent-uid", "event")
	if err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}
}

func TestMarkResourceDirty_ReturnsCalendarLookupError(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	err = MarkResourceDirty(context.Background(), db, 1, "uid", "event")
	if err == nil {
		t.Fatal("MarkResourceDirty err = nil, want database error")
	}
}

func TestMarkResourceDirty_SkipsZeroCalendarOrEmptyUID(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := MarkResourceDirty(ctx, db, 0, "uid", "event"); err != nil {
		t.Errorf("should skip when calendarID=0: %v", err)
	}
	if err := MarkResourceDirty(ctx, db, 1, "", "event"); err != nil {
		t.Errorf("should skip when uid is empty: %v", err)
	}
}

func TestCreateTombstoneIfSynced_NoopWhenNotSynced(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	created, err := CreateTombstoneIfSynced(context.Background(), db, 1, "no-sync-resource")
	if err != nil {
		t.Fatalf("CreateTombstoneIfSynced: %v", err)
	}
	if created {
		t.Error("should not create tombstone when no sync resource exists")
	}
}

func TestCreateTombstoneIfSynced_ReturnsSyncResourceLookupError(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	created, err := CreateTombstoneIfSynced(context.Background(), db, 1, "uid")
	if err == nil {
		t.Fatal("CreateTombstoneIfSynced err = nil, want database error")
	}
	if created {
		t.Fatal("CreateTombstoneIfSynced created = true, want false on database error")
	}
}

func TestCreateTombstoneIfSynced_SkipsZeroCalendarOrEmptyUID(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	created, err := CreateTombstoneIfSynced(ctx, db, 0, "uid")
	if err != nil || created {
		t.Errorf("should skip when calendarID=0: created=%v err=%v", created, err)
	}
	created, err = CreateTombstoneIfSynced(ctx, db, 1, "")
	if err != nil || created {
		t.Errorf("should skip when uid is empty: created=%v err=%v", created, err)
	}
}

func TestCreateTombstoneIfSynced_CreatesTombstone(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	// Create a sync resource with a remote URL
	err = q.UpsertSyncResource(ctx, UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          "tombstone-test-uid",
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/event.ics",
		Etag:         "etag-123",
		Dirty:        0,
		SyncStrategy: "sync-token",
	})
	if err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	created, err := CreateTombstoneIfSynced(ctx, db, calID, "tombstone-test-uid")
	if err != nil {
		t.Fatalf("CreateTombstoneIfSynced: %v", err)
	}
	if !created {
		t.Error("should have created tombstone for synced resource")
	}

	// Verify tombstone exists
	tombstones, err := q.ListTombstonesByCalendar(ctx, calID)
	if err != nil {
		t.Fatalf("ListTombstones: %v", err)
	}
	if len(tombstones) != 1 {
		t.Fatalf("expected 1 tombstone, got %d", len(tombstones))
	}
	if tombstones[0].Uid != "tombstone-test-uid" {
		t.Errorf("tombstone uid = %q, want %q", tombstones[0].Uid, "tombstone-test-uid")
	}
}

func TestCreateTombstoneIfSynced_DedupesRepeatedDeletes(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	err = q.UpsertSyncResource(ctx, UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          "dup-tombstone-uid",
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/event.ics",
		Etag:         "etag-123",
		Dirty:        0,
		SyncStrategy: "sync-token",
	})
	if err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// Delete the synced resource twice (e.g. delete, restore via sync, delete
	// again before a sync flush). Both calls must succeed and collapse onto a
	// single tombstone row for the (calendar_id, uid) pair.
	for i := 0; i < 2; i++ {
		created, err := CreateTombstoneIfSynced(ctx, db, calID, "dup-tombstone-uid")
		if err != nil {
			t.Fatalf("CreateTombstoneIfSynced (call %d): %v", i, err)
		}
		if !created {
			t.Fatalf("CreateTombstoneIfSynced (call %d) created = false, want true", i)
		}
	}

	tombstones, err := q.ListTombstonesByCalendar(ctx, calID)
	if err != nil {
		t.Fatalf("ListTombstones: %v", err)
	}
	if len(tombstones) != 1 {
		t.Fatalf("expected 1 tombstone after repeated deletes, got %d", len(tombstones))
	}
}

func TestMarkResourceDirty_SetsDirtyFlag(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	// Link calendar to an account so MarkResourceDirty acts
	account, err := q.CreateAccount(ctx, CreateAccountParams{
		Name: "test", ServerUrl: "https://example.com", AuthType: "basic", Username: "u",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	_ = q.LinkCalendarToAccount(ctx, LinkCalendarToAccountParams{
		AccountID: &account.ID, RemoteUrl: strPtr("https://example.com/cal/"), ID: calID,
	})

	// Mark dirty — should upsert a sync_resource row
	err = MarkResourceDirty(ctx, db, calID, "dirty-test-uid", "event")
	if err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}

	// Verify it's dirty
	dirty, err := q.ListDirtySyncResources(ctx, calID)
	if err != nil {
		t.Fatalf("ListDirty: %v", err)
	}
	if len(dirty) != 1 {
		t.Fatalf("expected 1 dirty resource, got %d", len(dirty))
	}
	if dirty[0].Uid != "dirty-test-uid" {
		t.Errorf("dirty uid = %q, want %q", dirty[0].Uid, "dirty-test-uid")
	}
}

func strPtr(s string) *string { return &s }
