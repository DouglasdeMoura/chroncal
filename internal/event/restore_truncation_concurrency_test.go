package event

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// purgeOnMasterReadDBTX wraps a DBTX. The first time it observes the
// GetEventByUID SELECT, it fires a hook *after* the row has been buffered. The
// hook purges the master from a separate connection. That simulates a concurrent
// purge/soft-delete that lands in the window between the master read and the
// in-transaction UPDATE that depends on it.
//
// restoreTruncationByLogID reads its master inside the write
// transaction (qtx.GetEventByUID on the *sql.Tx, not this wrapper). This hook
// is then only reachable when the read happens on the non-transactional q.
// That is the pre-fix code path. The fixed code never routes the master read
// through the wrapper. The injection cannot fire. No data is lost.
type purgeOnMasterReadDBTX struct {
	storage.DBTX
	once sync.Once
	hook func()
}

func (d *purgeOnMasterReadDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	// The underlying driver buffers the first row eagerly during
	// QueryRowContext, so firing the hook afterwards still lets the caller
	// Scan the master it read before the purge committed.
	row := d.DBTX.QueryRowContext(ctx, query, args...)
	if strings.Contains(query, "name: GetEventByUID :one") {
		d.once.Do(d.hook)
	}
	return row
}

// TestRestoreTruncation_ConcurrentPurgeNoSilentLoss is a regression test for
// issue #413. restoreTruncationByLogID read the master outside its
// transaction. It then used master.ID in an in-tx UPDATE. If the master was
// concurrently purged between the read and the UPDATE, the UPDATE matched 0
// rows (no error in SQLite). The truncation log row was still consumed. The
// transaction committed. The RRULE was never restored. The log that would
// let the user retry was gone. Silent, unrecoverable data loss.
//
// The test sets up a truncated recurring series. It then restores it through a
// service whose non-transactional queries are wrapped so that a concurrent
// purge fires immediately after the master read. The forbidden end state is
// "master gone AND truncation log gone". The truncation became unrecoverable
// without the RRULE ever being restored.
func TestRestoreTruncation_ConcurrentPurgeNoSilentLoss(t *testing.T) {
	// A file-backed DB is required so a second connection sees the same data.
	dbPath := filepath.Join(t.TempDir(), "issue413.db")
	db, q, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	svc := NewService(db, q)

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "issue413-series",
		CalendarID:     1,
		Title:          "Weekly Sync",
		StartTime:      time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	cutoff := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	if err := svc.DeleteFromInstance(ctx, master.UID, cutoff); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}

	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	var trunc TrashEntry
	found := false
	for _, e := range entries {
		if e.Kind == TrashKindTruncation {
			trunc = e
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no TrashKindTruncation entry, got %+v", entries)
	}

	// Second connection used to purge the master mid-restore.
	db2, _, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	t.Cleanup(func() { db2.Close() })

	wrapped := storage.New(&purgeOnMasterReadDBTX{
		DBTX: db,
		hook: func() {
			if _, err := db2.ExecContext(ctx,
				`DELETE FROM events WHERE uid = ? AND recurrence_id = ''`,
				master.UID); err != nil {
				t.Errorf("concurrent purge: %v", err)
			}
		},
	})
	raceSvc := NewService(db, wrapped)

	// Restore. Either it succeeds and restores the RRULE, or it fails and
	// leaves the log intact for a retry — but it must never silently consume
	// the log without restoring anything.
	restoreErr := raceSvc.RestoreTrash(ctx, trunc)

	masterGone := false
	if _, err := q.GetEventByUID(ctx, master.UID); err != nil {
		masterGone = true
	}

	logGone := true
	remaining, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash after restore: %v", err)
	}
	for _, e := range remaining {
		if e.Kind == TrashKindTruncation {
			logGone = false
		}
	}

	if masterGone && logGone {
		t.Fatalf("silent data loss: master was purged and the truncation log was consumed "+
			"without restoring the RRULE (restoreErr=%v); truncation is now unrecoverable", restoreErr)
	}
}
