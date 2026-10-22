package event

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// legacyLogFixture is a series from before migration 057. Its occurrence
// logs have the old form: no entry, no flag, no seq.
type legacyLogFixture struct {
	svc       *Service
	masterID  int64
	rowID     int64     // the override in the trash at withRow
	withRow   time.Time // an occurrence delete that hid rowID
	withNoRow time.Time // an occurrence delete with no stored override
}

// migratedLegacyLogs seeds the series at version 056 and migrates up. A
// migration test opens its own database: it must run the migration set
// (AGENTS.md, test speed rule 2).
func migratedLegacyLogs(t *testing.T) legacyLogFixture {
	t.Helper()
	ctx := context.Background()
	conn, q, err := storage.Open(t.TempDir() + "/legacy057.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	provider, err := storage.NewMigrationProvider(conn)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := provider.DownTo(ctx, 56); err != nil {
		t.Fatalf("down to 56: %v", err)
	}
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	f := legacyLogFixture{withRow: start.Add(24 * time.Hour), withNoRow: start.Add(48 * time.Hour)}
	x, y := f.withRow.Format(time.RFC3339), f.withNoRow.Format(time.RFC3339)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO events (id, uid, calendar_id, title, start_time, end_time, recurrence_rule, exdates)
		  VALUES (1, 'legacy', 1, 'Series', ?, ?, 'FREQ=DAILY;COUNT=4', ?)`,
			[]any{start.Format(time.RFC3339), start.Add(time.Hour).Format(time.RFC3339), x + "," + y}},
		{`INSERT INTO events (id, uid, calendar_id, title, start_time, end_time, recurrence_id, deleted_at)
		  VALUES (2, 'legacy', 1, 'Late', ?, ?, ?, '2026-05-01T00:00:00Z')`,
			[]any{f.withRow.Add(time.Hour).Format(time.RFC3339), f.withRow.Add(2 * time.Hour).Format(time.RFC3339), x}},
		{`INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (1, 'legacy', ?)`, []any{x}},
		{`INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (1, 'legacy', ?)`, []any{y}},
	} {
		if _, err := conn.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed at 056: %v", err)
		}
	}
	if _, err := provider.UpTo(ctx, 57); err != nil {
		t.Fatalf("up to 57: %v", err)
	}
	f.svc, f.masterID, f.rowID = NewService(conn, q), 1, 2
	return f
}

// restoreOccurrenceEntry restores the trash entry of the occurrence at.
func restoreOccurrenceEntry(t *testing.T, svc *Service, at time.Time) {
	t.Helper()
	entries, err := svc.ListTrash(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	for _, e := range entries {
		if e.Kind == TrashKindInstance && e.InstanceTime.Equal(at) {
			if err := svc.RestoreTrash(context.Background(), e); err != nil {
				t.Fatalf("RestoreTrash(%s): %v", at, err)
			}
			return
		}
	}
	t.Fatalf("trash holds no entry of the occurrence %s", at)
}

// TestMigration057_LegacyLogRestoreBringsBackTheOverride: the backfill names
// the override in the trash, so the restore from the trash brings it back
// and strips the EXDATE. A log with no override strips its EXDATE too.
func TestMigration057_LegacyLogRestoreBringsBackTheOverride(t *testing.T) {
	f := migratedLegacyLogs(t)
	restoreOccurrenceEntry(t, f.svc, f.withRow)
	wantTrashed(t, f.svc, false, f.rowID)
	if n := exdateCount(t, f.svc, f.masterID, f.withRow); n != 0 {
		t.Errorf("EXDATEs of the occurrence with the row after the restore = %d, want 0", n)
	}
	restoreOccurrenceEntry(t, f.svc, f.withNoRow)
	if n := exdateCount(t, f.svc, f.masterID, f.withNoRow); n != 0 {
		t.Errorf("EXDATEs of the occurrence with no row after the restore = %d, want 0", n)
	}
}

// TestMigration057_LegacyLogPurgeKeepsTheEXDATE: a purge of the override of a
// backfilled log keeps the EXDATE. The entry of the old delete stands.
func TestMigration057_LegacyLogPurgeKeepsTheEXDATE(t *testing.T) {
	f := migratedLegacyLogs(t)
	if err := f.svc.PurgeByID(context.Background(), f.rowID); err != nil {
		t.Fatalf("PurgeByID: %v", err)
	}
	if n := exdateCount(t, f.svc, f.masterID, f.withRow); n != 1 {
		t.Errorf("EXDATEs after the purge = %d, want 1", n)
	}
	log, found, err := occurrenceLogTx(context.Background(), f.svc.q, 1, "legacy", f.withRow.Format(time.RFC3339))
	if err != nil || !found || len(log.Entries) != 1 || !log.OwnsExdate {
		t.Errorf("log after the purge = %+v, %v, %v; want the old entry and the EXDATE", log, found, err)
	}
}

// TestMigration057_LaterDeleteUndoKeepsTheLegacyEntry: a later delete of the
// occurrence and its undo leave the entry of the old delete and the EXDATE.
// The override stays in the trash.
func TestMigration057_LaterDeleteUndoKeepsTheLegacyEntry(t *testing.T) {
	f := migratedLegacyLogs(t)
	ctx := context.Background()
	meta, err := f.svc.DeleteInstanceWithUndo(ctx, 1, "legacy", f.withRow)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	if n := exdateCount(t, f.svc, f.masterID, f.withRow); n != 1 {
		t.Errorf("EXDATEs after the later delete = %d, want 1", n)
	}
	if err := f.svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if n := exdateCount(t, f.svc, f.masterID, f.withRow); n != 1 {
		t.Errorf("EXDATEs after the undo = %d, want 1", n)
	}
	if ids, found := exdateLogRows(t, f.svc, "legacy", f.withRow); !found || len(ids) != 1 || ids[0] != f.rowID {
		t.Errorf("log rows after the undo = %v, %v; want the old entry with row %d", ids, found, f.rowID)
	}
	wantTrashed(t, f.svc, true, f.rowID)
}
