package event

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestDeleteInstance_ConcurrentNoLostUpdate is a regression test for issue
// #116: DeleteInstance read the master's EXDATE list outside the writing
// transaction, so two concurrent instance-deletes both computed their new
// list from the same pre-transaction snapshot and the second write silently
// clobbered the first — an excluded occurrence reappeared on next expansion.
//
// The test fires N concurrent DeleteInstance calls, each excluding a distinct
// occurrence of the same recurring master, and asserts that every EXDATE
// survives. Under the buggy read-modify-write the final list is short.
func TestDeleteInstance_ConcurrentNoLostUpdate(t *testing.T) {
	// A file-backed DB is required: ":memory:" gives every pooled connection
	// its own private database, which breaks once goroutines fan out across
	// connections.
	dbPath := filepath.Join(t.TempDir(), "issue116.db")
	db, q, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc := NewService(db, q)
	ctx := context.Background()

	start := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	master, err := svc.Create(ctx, CreateParams{
		CalendarID:     1,
		Title:          "Daily Standup",
		StartTime:      start,
		EndTime:        start.Add(15 * time.Minute),
		RecurrenceRule: "FREQ=DAILY;COUNT=30",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	const n = 12
	instances := make([]time.Time, n)
	for i := range instances {
		instances[i] = start.AddDate(0, 0, i)
	}

	var wg sync.WaitGroup
	gate := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate // release all goroutines together to maximize interleaving
			errs[i] = svc.DeleteInstance(ctx, master.UID, instances[i])
		}(i)
	}
	close(gate)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("DeleteInstance[%d]: %v", i, e)
		}
	}

	got, err := q.GetEventByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("reload master: %v", err)
	}
	exdates := ParseTimeList(storage.NullableToString(got.Exdates))
	have := make(map[time.Time]bool, len(exdates))
	for _, d := range exdates {
		have[d.UTC()] = true
	}

	missing := 0
	for _, want := range instances {
		if !have[want.UTC()] {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("lost %d of %d EXDATEs under concurrency (have %d): concurrent DeleteInstance clobbered the master EXDATE list",
			missing, n, len(exdates))
	}
}
