package alarm

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
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// isTodoAlarmFetch reports whether a query reads todo_alarms rows keyed by
// the todo. Both the per-todo ListFireableTodoAlarmsByTodoID and the
// batched ListFireableTodoAlarmsByTodoIDs match. A count of them measures
// the N+1 the batch removes. The window-sizing query
// ListDistinctTodoAlarmTriggers reads no todo_id, so it does not match.
func isTodoAlarmFetch(query string) bool {
	return strings.Contains(query, "FROM todo_alarms") && strings.Contains(query, "todo_id")
}

// countingDriver wraps another driver and counts the todo-alarm queries,
// so a test can assert the check loop issues one query, not one per todo.
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
	if isTodoAlarmFetch(query) {
		atomic.AddInt64(c.n, 1)
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

var countingDriverSeq atomic.Int64

// newCountingDB opens an in-memory database through the count driver. It
// runs the schema migrations. It returns the todo-alarm query counter.
func newCountingDB(t *testing.T) (*sql.DB, *storage.Queries, *int64) {
	t.Helper()
	var n int64
	name := fmt.Sprintf("sqlite-alarm-count-%d", countingDriverSeq.Add(1))
	sql.Register(name, countingDriver{base: &sqlitedrv.Driver{}, n: &n})

	conn, err := sql.Open(name, ":memory:?_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open counting db: %v", err)
	}
	// :memory: is per-connection, so pin the pool to one connection or the
	// migrations and the queries would reach different empty databases.
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

// TestCheckTodos_BatchesAlarmFetch guards issue #586 item (e). The check
// loop runs on a timer over every open todo. It must read the alarms of
// the whole set in one query, not one query per todo.
func TestCheckTodos_BatchesAlarmFetch(t *testing.T) {
	conn, q, counter := newCountingDB(t)
	ctx := context.Background()
	todoSvc := todo.NewService(conn, q)
	evtSvc := event.NewService(conn, q)
	svc := NewService(conn, q, evtSvc, todoSvc)
	todoAlarms := NewTodoService(conn, q, todoSvc)

	now := time.Now().UTC()
	const todos = 5
	for i := 0; i < todos; i++ {
		due := now.Add(time.Duration(i+1) * time.Hour)
		td, err := todoSvc.Create(ctx, todo.CreateParams{
			CalendarID: 1,
			Summary:    fmt.Sprintf("Todo %d", i),
			DueDate:    due.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("create todo %d: %v", i, err)
		}
		if err := todoSvc.ReplaceAlarms(ctx, td.ID, []model.Alarm{{
			Action: "DISPLAY", TriggerValue: "-PT15M", Related: "START",
		}}); err != nil {
			t.Fatalf("replace alarms %d: %v", i, err)
		}
	}

	atomic.StoreInt64(counter, 0)
	if _, err := todoAlarms.CheckTodos(ctx, now); err != nil {
		t.Fatalf("CheckTodos: %v", err)
	}
	if got := atomic.LoadInt64(counter); got != 1 {
		t.Errorf("CheckTodos issued %d todo-alarm queries for %d todos, want 1 (no N+1)", got, todos)
	}

	atomic.StoreInt64(counter, 0)
	if _, _, err := svc.CheckMissed(ctx, now, StaleThreshold); err != nil {
		t.Fatalf("CheckMissed: %v", err)
	}
	if got := atomic.LoadInt64(counter); got != 1 {
		t.Errorf("CheckMissed issued %d todo-alarm queries for %d todos, want 1 (no N+1)", got, todos)
	}
}
