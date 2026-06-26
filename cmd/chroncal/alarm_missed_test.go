package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/spf13/cobra"
)

// TestAlarmMissed_RejectsNonPositiveDays guards against issue #140: a
// zero or negative --days value produced an empty/undefined lookback
// window instead of an invalid_input error. Validation runs before
// initApp, so the command must fail without ever touching the database.
func TestAlarmMissed_RejectsNonPositiveDays(t *testing.T) {
	for _, days := range []string{"0", "-1", "-5"} {
		t.Run("days="+days, func(t *testing.T) {
			cmd := alarmMissedCmd()
			if err := cmd.ParseFlags([]string{"--days", days}); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}

			err := cmd.RunE(cmd, nil)
			if err == nil {
				t.Fatalf("--days %s: want error, got nil", days)
			}
			var ce *cliError
			if !errors.As(err, &ce) {
				t.Fatalf("--days %s: want *cliError, got %T: %v", days, err, err)
			}
			if ce.Code != "invalid_input" {
				t.Fatalf("--days %s: code = %q, want invalid_input", days, ce.Code)
			}
		})
	}
}

// TestAlarmMissed_JSONIsFlatArray is the regression test for issue #433:
// "alarm missed -o json" emitted a map {"events":[...],"todos":[...]} while
// the sibling "alarm list"/"alarm check" commands emit a flat array. Scripts
// using the `... -o json | jq '.[]'` idiom broke on the inconsistent shape.
// The output must be a flat array of items, each carrying a "type" discriminator.
func TestAlarmMissed_JSONIsFlatArray(t *testing.T) {
	a := newAlarmTestApp(t)
	ctx := t.Context()

	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	// Event 3 days ago with a 15-min-before alarm: trigger is well past the
	// 24h stale threshold and never fired, so it counts as missed.
	start := time.Now().Add(-72 * time.Hour)
	evt, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: cal.ID,
		Title:      "Missed Meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := a.Events.ReplaceAlarms(ctx, evt.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "START"},
	}); err != nil {
		t.Fatalf("replace event alarms: %v", err)
	}

	// Todo 3 days ago with a missed alarm too.
	due := time.Now().Add(-72 * time.Hour)
	td, err := a.Todos.Create(ctx, todo.CreateParams{
		CalendarID: cal.ID,
		Summary:    "Missed Task",
		DueDate:    due.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	if err := a.Todos.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "START"},
	}); err != nil {
		t.Fatalf("replace todo alarms: %v", err)
	}

	prevFmt := outputFmt
	outputFmt = "json"
	t.Cleanup(func() { outputFmt = prevFmt })

	root := &cobra.Command{
		Use: "chroncal-test",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var loadErr error
			cfg, loadErr = config.Load()
			return loadErr
		},
	}
	missed := alarmMissedCmd()
	root.AddCommand(missed)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"missed", "--days", "7"})
	if err := root.Execute(); err != nil {
		t.Fatalf("alarm missed: %v", err)
	}

	// The wire shape must be a flat JSON array, not an object. A map shape
	// fails to unmarshal into a slice, which is exactly what scripts hit.
	var items []map[string]any
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("alarm missed JSON is not a flat array: %v\noutput:\n%s", err, out.String())
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (one event, one todo):\n%s", len(items), out.String())
	}

	types := map[string]int{}
	for _, it := range items {
		typ, _ := it["type"].(string)
		types[typ]++
	}
	if types["event"] != 1 || types["todo"] != 1 {
		t.Fatalf("type discriminators = %v, want one event and one todo:\n%s", types, out.String())
	}

	if strings.Contains(out.String(), `"events"`) || strings.Contains(out.String(), `"todos"`) {
		t.Fatalf("output still uses map shape with events/todos keys:\n%s", out.String())
	}
}
