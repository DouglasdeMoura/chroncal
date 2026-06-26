package main

import (
	"bytes"
	"context"
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
	pushCalendarAfterWrite = func(a *app.App, calendarID int64, w io.Writer) {
		*pushed = append(*pushed, calendarID)
	}
	t.Cleanup(func() { pushCalendarAfterWrite = prev })
	return pushed
}

// runWriteCommand executes one CLI command tree in-process against the test
// DB. It wraps the resource command in a fresh parent so cfg is reloaded
// from CHRONCAL_DB (mirroring rootCmd's PersistentPreRunE) and so each call
// gets fresh flag closures.
func runWriteCommand(t *testing.T, sub *cobra.Command, args ...string) error {
	t.Helper()
	root := &cobra.Command{
		Use: "chroncal-test",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cfg = config.Load()
			return nil
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
// write paths invoke the opportunistic CalDAV push seam, matching event write
// paths. Regression guard for issue #115.
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

func assertPushed(t *testing.T, pushed []int64, want int64) {
	t.Helper()
	for _, id := range pushed {
		if id == want {
			return
		}
	}
	t.Fatalf("write path did not opportunistically push calendar %d; pushed=%v", want, pushed)
}
