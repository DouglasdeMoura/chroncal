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

	// Should be a no-op when no sync_resources row exists
	err = MarkResourceDirty(context.Background(), db, 1, "non-existent-uid")
	if err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}
}

func TestMarkResourceDirty_SkipsZeroCalendarOrEmptyUID(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := MarkResourceDirty(ctx, db, 0, "uid"); err != nil {
		t.Errorf("should skip when calendarID=0: %v", err)
	}
	if err := MarkResourceDirty(ctx, db, 1, ""); err != nil {
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

func TestMarkResourceDirty_SetsDirtyFlag(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	// Create a clean sync resource
	err = q.UpsertSyncResource(ctx, UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          "dirty-test-uid",
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/event.ics",
		Etag:         "etag-abc",
		Dirty:        0,
		SyncStrategy: "sync-token",
	})
	if err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// Mark dirty
	err = MarkResourceDirty(ctx, db, calID, "dirty-test-uid")
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
