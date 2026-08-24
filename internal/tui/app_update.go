package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(clockTickMsg); ok {
		if msg.token != m.clockTickToken {
			return m, nil
		}
		m.clockTickScheduled = false
		m = m.refreshToday(time.Now())
		return m.scheduleClockTick()
	}
	if next, cmd, handled := m.dispatchLifecycleMsg(msg); handled {
		return next, cmd
	}

	// Global key bindings override any open dialog: ctrl+c / q always route
	// through the quit guard, and ? opens the help dialog. The quit confirm
	// itself is exempt so its y/n/esc keys keep working, and ? is a no-op
	// while the help dialog is already up (it handles its own close keys).
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		if newM, cmd, handled := m.interceptGlobalKeys(kp); handled {
			return newM, cmd
		}
	}

	if next, cmd, handled := m.routeOverlay(msg); handled {
		return next, cmd
	}

	return m.dispatchAppMsg(msg)
}
