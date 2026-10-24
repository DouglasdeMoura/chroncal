package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/testutil"
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
	if err := a.DB.QueryRowContext(ctx, `SELECT id FROM accounts WHERE name = 'push-test'`).Scan(&accountID); err != nil {
		res, err := a.DB.ExecContext(ctx, `INSERT INTO accounts (name, server_url, auth_type, username) VALUES ('push-test', 'https://example.invalid', 'basic', 'u')`)
		if err != nil {
			t.Fatalf("insert account: %v", err)
		}
		accountID, _ = res.LastInsertId()
	}
	if _, err := a.DB.ExecContext(ctx, `UPDATE calendars SET account_id = ?, remote_url = '/cal/' || id || '/' WHERE id = ?`, accountID, calendarID); err != nil {
		t.Fatalf("link calendar: %v", err)
	}
}

// TestRestorePushCalendarsOrder checks the push set after a restore of a
// legacy override: the calendar with the dirty row first, then the calendar
// with the tombstone. A calendar with no sync state of the UID, for example
// the calendar of another series of the UID, stays out.
func TestRestorePushCalendarsOrder(t *testing.T) {
	a, err := app.New(testutil.DBPath(t))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()
	ids := make([]int64, 0, 3)
	for _, name := range []string{"Tomb", "Other", "Dirty"} {
		cal, err := a.Calendars.Create(ctx, name, "#7C3AED", "")
		if err != nil {
			t.Fatalf("calendar create: %v", err)
		}
		ids = append(ids, cal.ID)
	}
	tomb, other, dirty := ids[0], ids[1], ids[2]
	for _, stmt := range []string{
		fmt.Sprintf(`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, dirty) VALUES (%d, 'u', 'event', '/d/u.ics', 1)`, dirty),
		fmt.Sprintf(`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, dirty) VALUES (%d, 'u', 'event', '/o/u.ics', 0)`, other),
		fmt.Sprintf(`INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (%d, 'u', '/t/u.ics')`, tomb),
	} {
		if _, err := a.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	got := restorePushCalendars(ctx, a, "u", []int64{tomb, other, 0, dirty, tomb})
	if len(got) != 2 || got[0] != dirty || got[1] != tomb {
		t.Errorf("push calendars = %v, want [%d %d]", got, dirty, tomb)
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
