package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/config"
)

// stubPushSeam replaces the opportunistic-push seam with a recorder that
// captures the calendar IDs write paths attempt to push. It restores the
// original on test cleanup.
func stubPushSeam(t *testing.T) *[]int64 {
	t.Helper()
	pushed := &[]int64{}
	prev := pushCalendarAfterWrite
	pushCalendarAfterWrite = func(a *app.App, calendarID int64, w, _ io.Writer) {
		*pushed = append(*pushed, calendarID)
	}
	t.Cleanup(func() { pushCalendarAfterWrite = prev })
	return pushed
}

// runWriteCommand executes one CLI command tree in-process against the test
// DB. It wraps the resource command in a fresh parent so cfg is reloaded
// from CHRONCAL_DB. That mirrors rootCmd's PersistentPreRunE. Each call
// then gets fresh flag closures.
func runWriteCommand(t *testing.T, sub *cobra.Command, args ...string) error {
	t.Helper()
	root := &cobra.Command{
		Use: "chroncal-test",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			cfg, err = config.Load()
			return err
		},
	}
	root.AddCommand(sub)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	return root.Execute()
}

// TestWritePathsOpportunisticallyPush asserts that todo, journal, and import
// write paths invoke the opportunistic CalDAV push seam. That matches event
// write paths. Regression guard for issue #115.
func TestWritePathsOpportunisticallyPush(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_ASSUME_YES", "1")

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	ctx := context.Background()
	cal, err := a.Calendars.Create(ctx, "Work", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	calID := cal.ID
	a.Close()

	t.Run("todo add", func(t *testing.T) {
		pushed := stubPushSeam(t)
		if err := runWriteCommand(t, todoCmd(),
			"todo", "add", "Ship release", "--calendar", "Work"); err != nil {
			t.Fatalf("todo add: %v", err)
		}
		assertPushed(t, *pushed, calID)
	})

	t.Run("todo update", func(t *testing.T) {
		pushed := stubPushSeam(t)
		if err := runWriteCommand(t, todoCmd(),
			"todo", "update", "1", "--summary", "Ship release v2"); err != nil {
			t.Fatalf("todo update: %v", err)
		}
		assertPushed(t, *pushed, calID)
	})

	t.Run("todo complete", func(t *testing.T) {
		pushed := stubPushSeam(t)
		if err := runWriteCommand(t, todoCmd(),
			"todo", "complete", "1"); err != nil {
			t.Fatalf("todo complete: %v", err)
		}
		assertPushed(t, *pushed, calID)
	})

	t.Run("todo delete", func(t *testing.T) {
		pushed := stubPushSeam(t)
		if err := runWriteCommand(t, todoCmd(),
			"todo", "delete", "1", "--yes"); err != nil {
			t.Fatalf("todo delete: %v", err)
		}
		assertPushed(t, *pushed, calID)
	})

	t.Run("journal add", func(t *testing.T) {
		pushed := stubPushSeam(t)
		if err := runWriteCommand(t, journalCmd(),
			"journal", "add", "Daily note", "--calendar", "Work"); err != nil {
			t.Fatalf("journal add: %v", err)
		}
		assertPushed(t, *pushed, calID)
	})

	t.Run("journal update", func(t *testing.T) {
		pushed := stubPushSeam(t)
		if err := runWriteCommand(t, journalCmd(),
			"journal", "update", "1", "--summary", "Daily note v2"); err != nil {
			t.Fatalf("journal update: %v", err)
		}
		assertPushed(t, *pushed, calID)
	})

	t.Run("journal delete", func(t *testing.T) {
		pushed := stubPushSeam(t)
		if err := runWriteCommand(t, journalCmd(),
			"journal", "delete", "1", "--yes"); err != nil {
			t.Fatalf("journal delete: %v", err)
		}
		assertPushed(t, *pushed, calID)
	})

	t.Run("ical import", func(t *testing.T) {
		icsPath := filepath.Join(t.TempDir(), "in.ics")
		ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
			"BEGIN:VTODO\r\nUID:import-todo-1\r\nSUMMARY:Imported task\r\nEND:VTODO\r\n" +
			"END:VCALENDAR\r\n"
		if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
			t.Fatalf("write ics: %v", err)
		}
		pushed := stubPushSeam(t)
		if err := runWriteCommand(t, icalCmd(),
			"ical", "import", icsPath, "--calendar", "Work"); err != nil {
			t.Fatalf("ical import: %v", err)
		}
		assertPushed(t, *pushed, calID)
	})
}

// TestRestorePathsOpportunisticallyPush restores an event, a todo, and a
// journal by ID and by UID. Each restore must push the calendar of the row,
// as the delete does (issue #885). Before the fix the server changed only at
// the next sync.
func TestRestorePathsOpportunisticallyPush(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_ASSUME_YES", "1")

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	cal, err := a.Calendars.Create(context.Background(), "Work", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	// The calendar has an account link, so a restore leaves a dirty sync
	// row there. The push seam is a stub, so no server is needed.
	linkCLICalendar(t, a, cal.ID)
	a.Close()

	for _, tc := range []struct {
		name  string
		cmd   func() *cobra.Command
		add   []string
		table string
	}{
		{"event", eventCmd, []string{"event", "add", "Standup", "--calendar", "Work", "--date", "2026-05-18", "--time", "09:00", "--duration", "30m"}, "events"},
		{"todo", todoCmd, []string{"todo", "add", "Ship", "--calendar", "Work"}, "todos"},
		{"journal", journalCmd, []string{"journal", "add", "Note", "--calendar", "Work"}, "journals"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubPushSeam(t)
			if err := runWriteCommand(t, tc.cmd(), tc.add...); err != nil {
				t.Fatalf("%s add: %v", tc.name, err)
			}
			a, err := app.New(dbPath)
			if err != nil {
				t.Fatalf("app.New: %v", err)
			}
			var id int64
			var uid string
			if err := a.DB.QueryRowContext(context.Background(), `SELECT id, uid FROM `+tc.table+` ORDER BY id DESC LIMIT 1`).Scan(&id, &uid); err != nil {
				t.Fatalf("read row: %v", err)
			}
			a.Close()
			for _, ref := range []string{fmt.Sprint(id), uid} {
				if err := runWriteCommand(t, tc.cmd(), tc.name, "delete", fmt.Sprint(id), "--yes"); err != nil {
					t.Fatalf("%s delete: %v", tc.name, err)
				}
				pushed := stubPushSeam(t)
				if err := runWriteCommand(t, tc.cmd(), tc.name, "restore", ref); err != nil {
					t.Fatalf("%s restore %s: %v", tc.name, ref, err)
				}
				assertPushed(t, *pushed, cal.ID)
			}
		})
	}
}

// linkCLICalendar gives calendarID an account link with no server.
func linkCLICalendar(t *testing.T, a *app.App, calendarID int64) {
	t.Helper()
	ctx := context.Background()
	var accountID int64
	err := a.DB.QueryRowContext(ctx, `SELECT id FROM accounts WHERE name = 'push-test'`).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := a.DB.ExecContext(ctx, `INSERT INTO accounts (name, server_url, auth_type, username) VALUES ('push-test', 'https://example.invalid', 'basic', 'u')`)
		if err != nil {
			t.Fatalf("insert account: %v", err)
		}
		if accountID, err = res.LastInsertId(); err != nil {
			t.Fatalf("account id: %v", err)
		}
	} else if err != nil {
		t.Fatalf("read account: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx, `UPDATE calendars SET account_id = ?, remote_url = '/cal/' || id || '/' WHERE id = ?`, accountID, calendarID); err != nil {
		t.Fatalf("link calendar: %v", err)
	}
}

// restorePushFixture links one calendar for each name of names, runs the
// seed statements (%[1]d, %[2]d, ... name the calendars in order), and
// returns the calendar IDs and the ID of the row with the title "Row".
func restorePushFixture(t *testing.T, names []string, seed ...string) (string, []int64, int64) {
	t.Helper()
	dbPath := setupCalendarCLITestEnv(t)
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()
	ids := make([]int64, 0, len(names))
	args := make([]any, 0, len(names))
	for _, name := range names {
		cal, err := a.Calendars.Create(ctx, name, "#7C3AED", "")
		if err != nil {
			t.Fatalf("calendar create: %v", err)
		}
		linkCLICalendar(t, a, cal.ID)
		ids = append(ids, cal.ID)
		args = append(args, cal.ID)
	}
	for _, stmt := range seed {
		stmt = fmt.Sprintf(stmt, args...)
		if _, err := a.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	var rowID int64
	if err := a.DB.QueryRowContext(ctx, `SELECT id FROM events WHERE title = 'Row'`).Scan(&rowID); err != nil {
		t.Fatalf("read row: %v", err)
	}
	return dbPath, ids, rowID
}

// recordRestorePushes deletes the row rowID, then restores it and returns
// the calendars that the restore pushes. A pass of the stub push clears
// the dirty flag of the calendar, as a real push does.
func recordRestorePushes(t *testing.T, rowID int64, pass bool) []int64 {
	t.Helper()
	stubPushSeam(t)
	if err := runWriteCommand(t, eventCmd(), "event", "delete", fmt.Sprint(rowID), "--yes"); err != nil {
		t.Fatalf("event delete: %v", err)
	}
	var pushed []int64
	prev := pushCalendarAfterWrite
	pushCalendarAfterWrite = func(a *app.App, calendarID int64, _, _ io.Writer) {
		pushed = append(pushed, calendarID)
		if pass {
			if _, err := a.DB.ExecContext(context.Background(), `UPDATE sync_resources SET dirty = 0 WHERE calendar_id = ? AND uid = 'u'`, calendarID); err != nil {
				t.Errorf("clear dirty: %v", err)
			}
		}
	}
	t.Cleanup(func() { pushCalendarAfterWrite = prev })
	if err := runWriteCommand(t, eventCmd(), "event", "restore", fmt.Sprint(rowID)); err != nil {
		t.Fatalf("event restore: %v", err)
	}
	return pushed
}

// TestRestoreOfALegacyOverridePushesTheTargetFirst restores a legacy
// override. The restore marks the calendar of the master dirty and keeps
// the tombstone in the calendar of the row. The push sends the master
// calendar first. It sends the calendar of the row only after the master
// calendar holds the item, because that push deletes a server object.
func TestRestoreOfALegacyOverridePushesTheTargetFirst(t *testing.T) {
	for _, pass := range []bool{true, false} {
		t.Run(fmt.Sprintf("target push passes=%v", pass), func(t *testing.T) {
			_, ids, rowID := restorePushFixture(t, []string{"Master", "Legacy"},
				`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_rule) VALUES ('u', %[1]d, 'Series', '2026-05-11T09:00:00Z', '2026-05-11T10:00:00Z', 'FREQ=WEEKLY')`,
				`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id) VALUES ('u', %[2]d, 'Row', '2026-05-18T09:30:00Z', '2026-05-18T10:30:00Z', '2026-05-18T09:00:00Z')`,
				`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag) VALUES (%[1]d, 'u', 'event', '/cal/m/u.ics', 'e1')`,
				// The old server object of the legacy row waits for its delete.
				`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (%[2]d, 'u', '/cal/l/u.ics')`,
			)
			master, legacy := ids[0], ids[1]
			want := []int64{master}
			if pass {
				want = append(want, legacy)
			}
			if got := recordRestorePushes(t, rowID, pass); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("pushed = %v, want %v (Master %d, Legacy %d)", got, want, master, legacy)
			}
		})
	}
}

// TestRestorePushLeavesAnotherSeriesOfTheUID restores an override of a
// series. Another calendar holds another series of the UID with a pending
// edit (issue #842). The push of the restore sends the calendar of the
// restored series only.
func TestRestorePushLeavesAnotherSeriesOfTheUID(t *testing.T) {
	_, ids, rowID := restorePushFixture(t, []string{"Mine", "Other"},
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_rule) VALUES ('u', %[1]d, 'Series', '2026-05-11T09:00:00Z', '2026-05-11T10:00:00Z', 'FREQ=WEEKLY')`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id) VALUES ('u', %[1]d, 'Row', '2026-05-18T09:30:00Z', '2026-05-18T10:30:00Z', '2026-05-18T09:00:00Z')`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_rule) VALUES ('u', %[2]d, 'Other', '2026-05-11T12:00:00Z', '2026-05-11T13:00:00Z', 'FREQ=DAILY')`,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag) VALUES (%[1]d, 'u', 'event', '/cal/m/u.ics', 'e1')`,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty) VALUES (%[2]d, 'u', 'event', '/cal/o/u.ics', 'e2', 1)`,
	)
	if got := recordRestorePushes(t, rowID, true); fmt.Sprint(got) != fmt.Sprint([]int64{ids[0]}) {
		t.Errorf("pushed = %v, want [%d] (Other %d)", got, ids[0], ids[1])
	}
}

func assertPushed(t *testing.T, pushed []int64, want int64) {
	t.Helper()
	for _, id := range pushed {
		if id == want {
			return
		}
	}
	t.Fatalf("write path did not opportunistically push calendar %d; pushed=%v", want, pushed)
}

// TestMoveWritePathsPushBothCalendars moves an event, a todo, and a journal
// to another calendar. The move leaves a tombstone in the source calendar,
// so the write path must push the target and then the source. Before the
// fix the source delete waited for the next full sync.
func TestMoveWritePathsPushBothCalendars(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	ctx := context.Background()
	work, err := a.Calendars.Create(ctx, "MoveFrom", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	personal, err := a.Calendars.Create(ctx, "MoveTo", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	a.Close()

	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		add  []string
		move []string
	}{
		{"event", eventCmd, []string{"event", "add", "Standup", "--calendar", "MoveFrom", "--date", "2026-05-18", "--time", "09:00", "--duration", "30m"}, []string{"event", "update", "1", "--calendar", "MoveTo"}},
		{"todo", todoCmd, []string{"todo", "add", "Ship", "--calendar", "MoveFrom"}, []string{"todo", "update", "1", "--calendar", "MoveTo"}},
		{"journal", journalCmd, []string{"journal", "add", "Note", "--calendar", "MoveFrom"}, []string{"journal", "update", "1", "--calendar", "MoveTo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubPushSeam(t)
			if err := runWriteCommand(t, tc.cmd(), tc.add...); err != nil {
				t.Fatalf("%s add: %v", tc.name, err)
			}
			pushed := stubPushSeam(t)
			if err := runWriteCommand(t, tc.cmd(), tc.move...); err != nil {
				t.Fatalf("%s update --calendar: %v", tc.name, err)
			}
			if len(*pushed) != 2 || (*pushed)[0] != personal.ID || (*pushed)[1] != work.ID {
				t.Fatalf("pushed = %v, want [%d %d] (the target, then the source)", *pushed, personal.ID, work.ID)
			}
		})
	}
}
