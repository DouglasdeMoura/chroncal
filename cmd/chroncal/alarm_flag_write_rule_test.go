package main

import (
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// The create commands validate the parsed alarms with
// model.PrepareAlarmsForWrite before they create the event or the todo. A
// rejection after the create would leave a row the next run duplicates
// (issue #585).
//
// The flag syntax cannot state an alarm the service rule rejects today:
// parseOneAlarm assigns a fireable action and a START or END anchor
// itself. This test pins that agreement. A later change that loosens the
// flag parser fails here, before it can produce an orphaned row.
func TestParseAlarmFlags_OutputPassesTheServiceWriteRule(t *testing.T) {
	flags := []string{
		"-PT15M",
		"+PT5M",
		"PT1H",
		"DISPLAY:-PT30M",
		"EMAIL:-PT1H",
		"AUDIO:-PT5M",
		"display:-PT10M",
		"-PT30M:Stand up",
		"-PT30M:Stand up:3:PT5M",
		"-PT30M:Stand up:3:PT5M:END",
		"-PT30M:Stand up:3:PT5M:START",
		"20260401T090000Z",
		"2026-04-01T09:00:00Z",
		// An unknown prefix is not an action. The parser keeps the whole
		// value as the trigger, so the action stays DISPLAY.
		"PROCEDURE:-PT15M",
	}

	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			alarms, err := parseAlarmFlags([]string{flag})
			if err != nil {
				t.Skipf("the parser rejects %q, so the service rule never sees it: %v", flag, err)
			}
			if _, err := model.PrepareAlarmsForWrite(alarms); err != nil {
				t.Fatalf("the parser accepted %q but the service rule rejects it: %v"+
					" (a create would leave an orphaned row)", flag, err)
			}
		})
	}
}

// A value the service rule rejects must fail before any row is written.
// The check runs on the parsed slice, so it holds for every producer that
// reaches the create commands.
func TestPrepareAlarmsForWrite_RejectsABadAnchor(t *testing.T) {
	_, err := model.PrepareAlarmsForWrite([]model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "MIDDLE"},
	})
	if !errors.Is(err, model.ErrInvalidAlarm) {
		t.Fatalf("error = %v, want ErrInvalidAlarm", err)
	}
}
