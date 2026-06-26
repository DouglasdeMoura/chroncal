package recurrence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	sqlitedrv "modernc.org/sqlite"

	dbembed "github.com/douglasdemoura/chroncal/db"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// overrideQueryMarker is the SQL fragment shared by every override-fetch query
// (both the per-master ListOverridesByUID and the batched ListOverridesByUIDs).
// Counting queries that contain it measures the N+1 the fix eliminates.
const overrideQueryMarker = "recurrence_id != ''"

// countingDriver wraps another database/sql driver and counts executed override
// queries, so a test can assert the batched fetch issues one query, not N.
type countingDriver struct {
	base driver.Driver
	n    *int64
}

func (d countingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return countingConn{Conn: c, n: d.n}, nil
}

type countingConn struct {
	driver.Conn
	n *int64
}

func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, overrideQueryMarker) {
		atomic.AddInt64(c.n, 1)
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

var countingDriverSeq atomic.Int64

// newCountingDB opens an in-memory database through the counting driver, runs
// the schema migrations, and returns the override-query counter.
func newCountingDB(t *testing.T) (*sql.DB, *storage.Queries, *int64) {
	t.Helper()
	var n int64
	name := fmt.Sprintf("sqlite-count-%d", countingDriverSeq.Add(1))
	sql.Register(name, countingDriver{base: &sqlitedrv.Driver{}, n: &n})

	conn, err := sql.Open(name, ":memory:?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open counting db: %v", err)
	}
	// :memory: is per-connection, so pin the pool to one connection or migrations
	// and queries would hit different empty databases.
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })

	migrationsFS, err := fs.Sub(dbembed.Migrations, "migrations")
	if err != nil {
		t.Fatalf("sub migrations: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationsFS)
	if err != nil {
		t.Fatalf("goose provider: %v", err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conn, storage.New(conn), &n
}

// TestListExpandedByDateRange_BatchesOverrideFetch is the regression guard for
// issue #257: expanding many recurring masters must fetch all overrides in a
// single query rather than one per master.
func TestListExpandedByDateRange_BatchesOverrideFetch(t *testing.T) {
	conn, q, counter := newCountingDB(t)
	eventsSvc := event.NewService(conn, q)
	recurSvc := NewService(conn, q)
	ctx := context.Background()

	const masters = 5
	for i := 0; i < masters; i++ {
		master, err := eventsSvc.Create(ctx, event.CreateParams{
			CalendarID:     1,
			Title:          fmt.Sprintf("Weekly %d", i),
			StartTime:      time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
			EndTime:        time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC),
			RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO",
		})
		if err != nil {
			t.Fatalf("create master %d: %v", i, err)
		}
		// Override the Jan 12 occurrence, moved one hour later, in-window.
		_, err = eventsSvc.UpsertByUID(ctx, event.UpsertParams{
			UID:          master.UID,
			CalendarID:   1,
			Title:        fmt.Sprintf("Weekly %d (moved)", i),
			StartTime:    time.Date(2026, 1, 12, 10, 0, 0, 0, time.UTC),
			EndTime:      time.Date(2026, 1, 12, 11, 0, 0, 0, time.UTC),
			RecurrenceID: "2026-01-12T09:00:00Z",
		})
		if err != nil {
			t.Fatalf("create override %d: %v", i, err)
		}
	}

	from := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 19, 0, 0, 0, 0, time.UTC)

	atomic.StoreInt64(counter, 0)
	events, err := recurSvc.ListExpandedByDateRange(ctx, from, to)
	if err != nil {
		t.Fatalf("ListExpandedByDateRange: %v", err)
	}

	if got := atomic.LoadInt64(counter); got != 1 {
		t.Errorf("override fetch issued %d queries for %d masters, want 1 (no N+1)", got, masters)
	}

	// Behavior check: each master yields two in-window occurrences (Jan 5 master
	// + Jan 12 override), and the overridden Jan 12 slot is not double-counted.
	moved := 0
	for _, e := range events {
		if strings.Contains(e.Title, "(moved)") {
			moved++
			if !e.StartTime.Equal(time.Date(2026, 1, 12, 10, 0, 0, 0, time.UTC)) {
				t.Errorf("moved override start = %v, want 10:00", e.StartTime)
			}
		}
	}
	if moved != masters {
		t.Errorf("got %d moved overrides, want %d", moved, masters)
	}
	if len(events) != masters*2 {
		t.Errorf("got %d events, want %d (2 per master)", len(events), masters*2)
	}
}
