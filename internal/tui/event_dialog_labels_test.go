package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/help"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// TestEventDialog_NarrowLabelsEndInOneColumn covers issue #920. At 80x24
// the day dialog uses the narrow layout. Each label of the detail pane
// ends in the same column, and the pane names the alarms "Alarms:", as
// the event view does.
func TestEventDialog_NarrowLabelsEndInOneColumn(t *testing.T) {
	day := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	ev := event.Event{
		ID:            1,
		CalendarID:    1,
		Title:         "Team sync",
		StartTime:     time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
		ConferenceURI: "https://meet.example.com/abc",
		Alarms:        []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}},
	}
	cals := map[int64]CalendarInfo{1: {Name: "Work"}}
	m := NewEventDialogModel(day, []event.Event{ev}, cals, help.New()).SetSize(80, 24)
	require.True(t, m.shell.isNarrow(), "80 columns must use the narrow layout")

	lines, _ := eventDetailLines(ev, cals[1], m.detailWidth(), m.labelWidth(), "", nil)
	ends := map[string]int{}
	plain := make([]string, 0, len(lines))
	for _, line := range lines {
		text := stripANSI(line)
		plain = append(plain, text)
		for _, label := range []string{"When", "Duration", "Calendar", "Conference"} {
			if i := strings.Index(text, label+"  "); i >= 0 && strings.TrimSpace(text[:i]) == "" {
				ends[label] = i + len(label)
			}
		}
	}
	require.Len(t, ends, 4, "the pane lost a label:\n%s", strings.Join(plain, "\n"))
	for label, end := range ends {
		assert.Equal(t, ends["When"], end, "label %q ends in another column:\n%s", label, strings.Join(plain, "\n"))
	}
	assert.Contains(t, plain, "Alarms:")
	assert.NotContains(t, strings.Join(plain, "\n"), "Reminders")
}
