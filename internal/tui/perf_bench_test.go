package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"

	"charm.land/bubbles/v2/help"
	lipgloss "charm.land/lipgloss/v2"
)

func benchEvents(n int) []event.Event {
	evs := make([]event.Event, n)
	base := time.Date(2026, 5, 30, 0, 0, 0, 0, time.Local)
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * 15 * time.Minute)
		evs[i] = event.Event{
			ID:        int64(i + 1),
			Title:     fmt.Sprintf("Slot %02d", i),
			StartTime: ts,
			EndTime:   ts.Add(15 * time.Minute),
		}
	}
	return evs
}

// TestEventDialogFramedDimensions guards against the manual frame
// drifting out of sync with BoxSize, which would let the dialog over-
// or under-paint by a row/column.
func TestEventDialogFramedDimensions(t *testing.T) {
	evs := benchEvents(96)
	day := time.Date(2026, 5, 30, 0, 0, 0, 0, time.Local)
	m := NewEventDialogModel(day, evs, map[int64]CalendarInfo{}, help.New()).SetSize(120, 40)
	v := m.View()
	bw, bh := m.BoxSize()
	lines := strings.Split(v, "\n")
	if len(lines) != bh {
		t.Errorf("rendered height = %d, BoxSize height = %d", len(lines), bh)
	}
	maxW := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > maxW {
			maxW = w
		}
	}
	if maxW != bw {
		t.Errorf("rendered width = %d, BoxSize width = %d", maxW, bw)
	}
}

func BenchmarkEventDialogView(b *testing.B) {
	evs := benchEvents(96)
	day := time.Date(2026, 5, 30, 0, 0, 0, 0, time.Local)
	m := NewEventDialogModel(day, evs, map[int64]CalendarInfo{}, help.New()).SetSize(120, 40)
	_ = m.View()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}
