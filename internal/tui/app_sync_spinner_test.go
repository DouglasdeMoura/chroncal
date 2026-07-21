package tui

import (
	"testing"

	"charm.land/bubbles/v2/spinner"
)

// TestSyncSpinnerTickNotBlockedByOverlays verifies that spinner.TickMsg always
// reaches m.syncSpinner.Update even when a palette, event-form, or
// calendar-dialog overlay is open. These overlays capture input, but a
// spinner.TickMsg is a background animation tick — blocking it kills the
// footer sync-spinner for the rest of that sync operation.
//
// Regression test for issue #348.
func TestSyncSpinnerTickNotBlockedByOverlays(t *testing.T) {
	// spinner.TickMsg with zero ID is accepted by any spinner (the ID check
	// inside spinner.Update is "ID > 0 && ID != m.id", so ID==0 bypasses it).
	tick := spinner.TickMsg{}

	overlays := []struct {
		name  string
		setup func(*Model)
	}{
		{
			name: "paletteOpen",
			setup: func(m *Model) {
				m.paletteOpen = true
			},
		},
		{
			name: "formOpen",
			setup: func(m *Model) {
				m.formOpen = true
			},
		},
		{
			name: "calendarManagerOpen",
			setup: func(m *Model) {
				m.calendarManagerOpen = true
			},
		},
	}

	for _, ov := range overlays {
		t.Run(ov.name, func(t *testing.T) {
			sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
			m := Model{
				syncing:     true,
				syncSpinner: sp,
			}
			ov.setup(&m)

			_, cmd := m.Update(tick)
			if cmd == nil {
				t.Fatalf("%s: spinner.TickMsg broke the sync-spinner tick chain (cmd == nil)", ov.name)
			}
		})
	}
}
