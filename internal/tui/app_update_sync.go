package tui

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

func (m Model) handleSyncAllRequested(msg SyncAllRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing {
		return m, nil
	}
	m.syncing = true
	m.statusToken++
	m.syncStatus = "Preparing sync…"
	m.syncTargets = nil
	m.syncTotals = syncTotals{}
	return m, tea.Batch(m.runSyncAllPlan(), m.syncSpinner.Tick)
}

func (m Model) handleSyncAllPlanned(msg syncAllPlannedMsg) (tea.Model, tea.Cmd) {
	if len(msg.targets) == 0 {
		m.syncing = false
		m.statusToken++
		m.syncStatus = "No connected calendars to sync"
		return m, m.expireStatusAfter(6*time.Second, m.statusToken)
	}
	m.syncTargets = msg.targets
	m.syncTotals = syncTotals{}
	first := msg.targets[0]
	m.statusToken++
	m.syncStatus = fmt.Sprintf("Syncing %s (1/%d)…", syncProgressLabel(first.Name), len(msg.targets))
	return m, m.runSyncOne(first, 0, len(msg.targets))
}

func (m Model) handleSyncCalendarFinished(msg syncCalendarFinishedMsg) (tea.Model, tea.Cmd) {
	if msg.result != nil {
		m.syncTotals.pushed += msg.result.Pushed
		m.syncTotals.pulled += msg.result.Pulled
		m.syncTotals.deleted += msg.result.Deleted
		m.syncTotals.conflicts += msg.result.Conflicts
		m.syncTotals.autoResolved += msg.result.AutoResolved
		m.syncTotals.skippedConflicts += msg.result.SkippedConflicts
		m.syncTotals.warnings += len(msg.result.Warnings)
		m.syncTotals.errCount += len(msg.result.Errors)
		if m.syncTotals.firstErr == nil && len(msg.result.Errors) > 0 {
			m.syncTotals.firstErr = msg.result.Errors[0]
		}
	}
	if msg.err != nil {
		m.syncTotals.errCount++
		if m.syncTotals.firstErr == nil {
			m.syncTotals.firstErr = msg.err
		}
	}
	next := msg.index + 1
	if next >= msg.total {
		label := fmt.Sprintf("%d calendar(s)", msg.total)
		summary := syncSummary(label, m.syncTotals)
		finishErr := m.syncTotals.firstErr
		m.syncTargets = nil
		m.syncTotals = syncTotals{}
		return m, func() tea.Msg {
			return syncFinishedMsg{summary: summary, err: finishErr, reload: true}
		}
	}
	target := m.syncTargets[next]
	m.statusToken++
	m.syncStatus = fmt.Sprintf("Syncing %s (%d/%d)…", syncProgressLabel(target.Name), next+1, msg.total)
	return m, m.runSyncOne(target, next, msg.total)
}

func (m Model) handleSyncCalendarRequested(msg SyncCalendarRequestedMsg) (tea.Model, tea.Cmd) {
	if m.syncing {
		// A sync is already running (e.g. re-auth finished mid-sync).
		// Queue this one so it isn't lost — syncFinishedMsg drains it,
		// guaranteeing the post-reauth sync runs and the ⚠ clears.
		m.pendingSyncCalendar = syncTarget(msg)
		return m, nil
	}
	m.syncing = true
	m.statusToken++
	label := msg.Name
	if label == "" {
		label = "calendar"
	}
	m.syncStatus = fmt.Sprintf("Syncing %s…", label)
	return m, tea.Batch(m.runSyncCalendar(msg.ID, msg.Name), m.syncSpinner.Tick)
}

func (m Model) handleTick(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	// Multiple spinners can be live simultaneously: the sync footer's, the
	// OAuth pending modal's, and the palette's search spinner. Each bubbles
	// spinner ignores ticks that aren't its own (ID check), so routing to
	// all active sub-components is safe. Spinner ticks are whitelisted
	// through every overlay guard so they always reach this handler rather
	// than being silently consumed by the overlay.
	var cmds []tea.Cmd
	if m.oauthFlowOpen {
		var c tea.Cmd
		m.oauthFlow, c = m.oauthFlow.Update(msg)
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	if m.paletteOpen {
		var c tea.Cmd
		m.palette, c = m.palette.Update(msg)
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	if m.syncing {
		var c tea.Cmd
		m.syncSpinner, c = m.syncSpinner.Update(msg)
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

func (m Model) handleOpportunisticPushFinished(msg opportunisticPushFinishedMsg) (tea.Model, tea.Cmd) {
	// Don't stomp the manual-sync status line or reset m.syncing.
	if m.syncing {
		return m, nil
	}
	if msg.err == nil && msg.summary == "" {
		return m, nil
	}
	m.statusToken++
	if msg.err != nil {
		if msg.summary != "" {
			m.syncStatus = fmt.Sprintf("%s — %s", msg.summary, msg.err.Error())
		} else {
			m.syncStatus = "Sync failed: " + msg.err.Error()
		}
	} else {
		m.syncStatus = msg.summary
	}
	return m, m.expireStatusAfter(4*time.Second, m.statusToken)
}
