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

	// A window below the minimum size shows only the notice. A key, a
	// paste, or a click then does nothing, so it cannot act on the hidden
	// layout. These keys stay:
	//   - ctrl+c goes to the quit guard, and the notice shows the quit
	//     confirm (see smallWindowNotice).
	//   - The keys of the quit confirm reach it.
	//   - esc stops a running sync or account discovery.
	if m.windowTooSmall() {
		switch in := msg.(type) {
		case tea.KeyPressMsg:
			inQuitConfirm := m.confirmOpen && m.pending.kind == pendingActionQuit
			if in.String() == "esc" && !inQuitConfirm {
				next, _ := m.cancelRunningOp()
				return next, nil
			}
			if in.String() != "ctrl+c" && !inQuitConfirm {
				return m, nil
			}
		case tea.PasteMsg, tea.MouseMsg:
			return m, nil
		}
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
