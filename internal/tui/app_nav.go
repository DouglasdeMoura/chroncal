package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/event"
)

func nextClockTickDelay(now time.Time) time.Duration {
	nextMinute := now.Truncate(time.Minute).Add(time.Minute)
	d := nextMinute.Sub(now)
	if d <= 0 {
		return time.Minute
	}
	return d
}

func nextClockTick(token int) tea.Cmd {
	return tea.Tick(nextClockTickDelay(time.Now()), func(time.Time) tea.Msg {
		return clockTickMsg{token: token}
	})
}

// scheduleClockTick arms the once-a-minute tick if one is not already armed.
// Every view runs it. Day and week need it to advance the now-line. All views
// need it so the "today" cell highlight follows the midnight day rollover.
func (m Model) scheduleClockTick() (Model, tea.Cmd) {
	if m.clockTickScheduled {
		return m, nil
	}
	m.clockTickToken++
	m.clockTickScheduled = true
	return m, nextClockTick(m.clockTickToken)
}

// refreshToday recomputes the local "today" date and propagates it into every
// view model. The views render their "today" highlight from these stored
// fields. Without this they freeze on the date captured at startup once the
// app is left open across midnight. Day view's grid and the sidebar mini-month
// recompute today at render time and are already immune. A refresh of the
// stored fields keeps week, month, and agenda current. A later view switch
// then carries the right date.
func (m Model) refreshToday(now time.Time) Model {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	m.day.today = today
	m.week.today = today
	m.agenda.today = today
	m.calendar.today = today
	return m
}

// navigateMainTo sets the active main view's cursor (and the month-view's
// displayed month) to the given date. Callers typically follow this with
// m.loadEvents() to refresh the query range.
func (m Model) navigateMainTo(t time.Time) Model {
	switch m.viewMode {
	case viewDay:
		m.day.cursor = t
	case viewWeek:
		m.week.cursor = t
	case viewAgenda:
		m.agenda.cursor = t
		m.agenda = m.agenda.ResetWindow(t)
	default:
		m.calendar.cursor = t
		m.calendar.month = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	}
	return m
}

// refreshCalendarViews recomputes the per-view CalendarEvent slices from the
// current m.events using the current m.hiddenCalendars set. Use this after the
// hidden set changes (no DB round-trip needed).
func (m Model) refreshCalendarViews() Model {
	calEvents := eventsToCalendar(m.events, m.calendars, m.hiddenCalendars)
	m.calendar = m.calendar.SetEvents(calEvents)
	m.week = m.week.SetEvents(calEvents)
	m.day = m.day.SetEvents(calEvents)
	m.agenda = m.agenda.SetEvents(filterVisibleEvents(m.events, m.hiddenCalendars), m.calendars)
	return m
}

// filterVisibleEvents drops events whose calendar is currently hidden. The
// agenda row list then stays in sync with the toggle set. The original slice
// is not mutated.

// clearConfirmPending drops wait state owned by a destructive confirm or
// choice. Used when ctrl+c abandons that operation in favor of the quit
// confirm. The superseded action can then never fire or reappear afterward.
func (m Model) clearConfirmPending() Model {
	m.pendingDelete = event.Event{}
	m.pendingPurgeEntries = nil
	m.pendingPurgeTitle = ""
	m.pendingCalendarDelete = 0
	m.pendingCalendarDeleteName = ""
	m.pendingCalendarPromote = 0
	m.pendingCalendarPromoteName = ""
	m.pendingCalendarPromoteCands = nil
	m.pendingAccountSelection = nil
	m.pendingAccountDefaultCandidates = nil
	m.pendingAccountRemoveID = 0
	m.pendingAccountRemoveName = ""
	if m.choiceOpen {
		m.pendingCalendarMove = nil
	}
	m.choiceOpen = false
	m.pendingScopeKind = pendingScopeNone
	return m
}

// openQuitConfirm builds and opens the quit-confirm dialog. Shared by the
// q and ctrl+c entry points so the two keystrokes cannot drift in style.
func (m Model) openQuitConfirm() Model {
	m.pendingQuit = true
	m.confirmDialog = NewConfirmDialogModel("Quit chroncal?", "Quit", m.theme).
		SetSize(m.width, m.height)
	m.confirmOpen = true
	return m
}

// interceptGlobalKeys routes the quit guard (q / ctrl+c) and help (?) ahead
// of any open dialog so they work from anywhere. A second ctrl+c while the
// quit confirm is on screen forces the exit. ctrl+c is truly global. It is
// not a character anyone types into a field. ? is suppressed while a
// text-entry surface owns input (palette search, event form, calendar form)
// so users can type it normally. q is suppressed while any overlay is open
// so the overlay's own close binding runs instead. The quit confirm also
// blocks ?. The help dialog handles its own close keys.
func (m Model) interceptGlobalKeys(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	inQuitConfirm := m.confirmOpen && m.pendingQuit
	if msg.String() == "ctrl+c" {
		if inQuitConfirm {
			m.oauthFlow.Abort() // release any in-flight OAuth listener
			return m, tea.Quit, true
		}
		// ctrl+c is truly global. Even when a destructive (non-quit)
		// confirm — event delete, trash purge, calendar delete — owns
		// input, replace it with the quit confirm. Do not let the
		// keystroke fall through to confirmDialog.Update (which ignores
		// it). Clear the abandoned confirm's pending state. That keeps the
		// destructive action from a later fire.
		m = m.clearConfirmPending()
		return m.openQuitConfirm(), nil, true
	}
	textEntryActive := m.paletteOpen || m.formOpen || m.calendarManagerOpen ||
		m.oauthFlowOpen || m.accountOAuthConfigOpen || m.accountCredentialsOpen
	// q opens the quit confirm only from the bare grid. Any open overlay
	// owns q. That includes text-entry (palette, form) and read-only/choice
	// (event view, list, choice, calendar list, trash, help). Its own
	// `q`-to-close key binding then runs, not the global quit guard
	// (issue #406).
	if key.Matches(msg, m.keys.Quit) && !m.anyOverlayOpen() {
		return m.openQuitConfirm(), nil, true
	}
	if key.Matches(msg, m.keys.Help) && !inQuitConfirm && !m.helpDialogOpen && !textEntryActive {
		return m, func() tea.Msg { return HelpDialogRequestedMsg{} }, true
	}
	// Trash: shift+D opens the Recently-deleted overlay from the main grid.
	// Blocked while a text-entry surface owns input (so typing "D" in the
	// palette / form doesn't jump out).
	if key.Matches(msg, m.keys.TrashView) && !m.trashOpen && !inQuitConfirm && !textEntryActive && !m.anyOverlayOpen() {
		m.trashOpen = true
		m.trash = NewTrashModel(m.calendars, newThemedHelp(m.theme)).
			SetSelectedColor(m.theme.Selected).
			SetSize(m.width, m.height)
		return m, m.loadTrash(), true
	}
	// Undo: only active on the main grid, with no overlay competing for input.
	if key.Matches(msg, m.keys.Undo) && m.undoIsAllowed() {
		entry, ok := m.undoStack.Peek()
		if ok {
			// Bumping the token invalidates any delete-push that was still
			// waiting for the 6-second window to elapse.
			m.pushDeferralToken++
			m.undoRestoreInFlight = true
			m.toast.Restoring()
			meta := entry.Meta
			title := meta.Label
			cmd := func() tea.Msg {
				err := m.app.Events.RestoreUndo(context.Background(), meta)
				return eventRestoredMsg{meta: meta, title: title, err: err}
			}
			return m, cmd, true
		}
	}
	return m, nil, false
}

// undoIsAllowed reports whether the `u` key should trigger an undo. The guard
// is intentionally strict. Any overlay, editor, or palette that might consume
// character input takes priority. Otherwise a stray `u` in a title field
// would trigger a restore in silence.
func (m Model) undoIsAllowed() bool {
	if m.focus != focusCalendar {
		return false
	}
	if m.anyOverlayOpen() {
		return false
	}
	if m.undoRestoreInFlight {
		return false
	}
	return m.undoStack != nil && m.undoStack.Len() > 0
}

// anyOverlayOpen reports whether any dialog, form, or palette is currently
// on screen. While one is up it owns input and renders its own help row.
// The app footer should then degrade to status plus toast rather than a
// duplicate of the dialog's hints.
func (m Model) anyOverlayOpen() bool {
	return m.paletteOpen || m.formOpen || m.viewDialogOpen || m.dialogOpen ||
		m.confirmOpen || m.choiceOpen || m.calendarManagerOpen ||
		m.accountRenameOpen || m.accountOAuthConfigOpen || m.accountCredentialsOpen ||
		m.helpDialogOpen || m.trashOpen || m.oauthFlowOpen
}
