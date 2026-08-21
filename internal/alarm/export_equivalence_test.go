package alarm

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
)

// The export invariant for absolute triggers: the exported TRIGGER and
// computeTriggerTimeForInstance must denote the same instant. Export resolves
// a floating value through model.ParseAbsoluteTime with the record's
// timezone, and this test reads the engine through the same function, so the
// two cannot drift (issue #572). A string-suffix check cannot catch drift,
// which is why the instants are compared here.
func TestExportedTriggerMatchesEngineFireTime(t *testing.T) {
	t.Parallel()
	const floating = "20260401T100000"
	expEvt := recurrence.ExpandedEvent{
		Event: event.Event{
			UID:       "engine-equivalence@example.com",
			Title:     "Event",
			Timezone:  "America/New_York",
			StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
			Alarms:    []model.Alarm{{Action: "DISPLAY", TriggerValue: floating, Related: "START"}},
		},
		InstanceTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}
	alarmRow := model.Alarm{Action: "DISPLAY", TriggerValue: floating, Related: "START"}

	engineAt, err := computeTriggerTimeForInstance(expEvt, alarmRow)
	if err != nil {
		t.Fatalf("computeTriggerTimeForInstance: %v", err)
	}

	data, err := ical.ExportEvents([]event.Event{expEvt.Event}, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	var raw string
	for _, line := range strings.Split(string(data), "\r\n") {
		if strings.HasPrefix(line, "TRIGGER") {
			if i := strings.Index(line, ":"); i >= 0 {
				raw = line[i+1:]
			}
		}
	}
	if raw == "" {
		t.Fatalf("no TRIGGER value in export:\n%s", data)
	}
	exportedAt, err := time.Parse("20060102T150405Z", raw)
	if err != nil {
		t.Fatalf("exported TRIGGER %q is not UTC date-time: %v", raw, err)
	}
	if !exportedAt.Equal(engineAt) {
		t.Errorf("exported TRIGGER %s and engine fire time %s differ", exportedAt, engineAt)
	}
}
