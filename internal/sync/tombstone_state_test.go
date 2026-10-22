package sync

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestApplyReadsTheTombstonesWhenItRuns models a local restore during a
// pull. The pull lists a tombstone of the UID, and the restore removes it
// before apply runs. The server then reports the object as deleted. apply
// must read the tombstones when it runs: with no tombstone, the local rows
// follow the server delete. A stale set drops the sync row of the live
// event and keeps the event, so local and server never agree again.
func TestApplyReadsTheTombstonesWhenItRuns(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	deletions := newPendingDeletions(engine.logger)
	deletions.markExplicit(sr)
	// The calendar holds no tombstone of the UID when apply runs.
	if _, failed, _ := deletions.apply(ctx, engine, calA, nil); failed != 0 {
		t.Fatalf("apply failed = %d, want 0", failed)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err == nil {
		t.Fatalf("event after the server delete = %+v, want it in the trash", got)
	}
}

// TestApplyKeepsAnAbsentUIDThatThePullSkipped models a local restore during
// a full pull. The pull skips the href of a tombstoned UID, so the UID is
// absent from the inventory. The restore then removes the tombstone. The
// absence proves nothing, because the pull did not look at the object, so
// apply must keep the restored event.
func TestApplyKeepsAnAbsentUIDThatThePullSkipped(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	deletions := newPendingDeletions(engine.logger)
	deletions.owner[evt.UID] = "event"
	// The pull skipped the UID: the tombstone was there when it started.
	skipped := map[string]bool{evt.UID: true}
	if _, failed, _ := deletions.apply(ctx, engine, calA, skipped); failed != 0 {
		t.Fatalf("apply failed = %d, want 0", failed)
	}
	if _, err := engine.events.Get(ctx, evt.ID); err != nil {
		t.Fatalf("restored event after the pull: %v; want it live", err)
	}
}
