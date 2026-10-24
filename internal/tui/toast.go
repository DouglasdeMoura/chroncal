package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ToastState names the four distinct things the undo toast can say. Callers
// transition between them; the model itself does not infer state, it just
// renders whatever state was set.
type ToastState int

const (
	ToastEmpty           ToastState = iota // nothing to show
	ToastDeletedUnsynced                   // "Deleted 'X' — undo (u)", with the undo key
	ToastDeletedSynced                     // "Deleted 'X' (synced) — undo (u)"
	ToastRestoring                         // "Restoring…"
	ToastRestored                          // "✓ Restored 'X'"
	ToastFailed                            // "<Action> failed: <reason>"
	ToastPurged                            // "✓ Purged 'X'"
)

// ToastAutoDismissDelay is the default 6-second window a deleted-toast lives
// before it fades. A failed-toast uses the shorter value below.
const ToastAutoDismissDelay = 6 * time.Second

// ToastFailedDismissDelay is the shorter window for a failed toast —
// errors read faster and overstay their welcome.
const ToastFailedDismissDelay = 4 * time.Second

// ToastRestoredDismissDelay is shorter than Deleted's window: by the time
// success shows, the user already saw the row reappear in the grid.
const ToastRestoredDismissDelay = 3 * time.Second

// toastTickMsg fires when a toast's auto-dismiss timer elapses. The token
// discriminates between generations so a replaced toast's old timer is
// ignored.
type toastTickMsg struct{ token int }

// ToastModel is a single-slot UI surface for the undo affordance. "Single
// slot, last wins" is the whole mental model. A second delete within the 6s
// window replaces the first toast. The undo stack (independent of the
// toast) still remembers both.
type ToastModel struct {
	state  ToastState
	title  string
	reason string
	// action names the action that failed in the failed toast, for
	// example "Undo" or "Restore".
	action   string
	token    int
	theme    Theme
	deadline time.Time
	// undoKey is the label of the undo key in the delete toast. A [keys]
	// remap of the undo action changes it (WithUndoKey).
	undoKey string
	// count is the number of items of a bulk restore or purge. A count
	// above one replaces the quoted title (issue #889).
	count int
}

// NewToastModel returns an empty toast bound to the given theme.
func NewToastModel(theme Theme) ToastModel {
	return ToastModel{theme: theme, state: ToastEmpty, undoKey: "u"}
}

// WithUndoKey sets the label of the undo key that the delete toast names.
func (t ToastModel) WithUndoKey(label string) ToastModel {
	t.undoKey = label
	return t
}

// Theme lets late theme switches propagate to the toast.
func (t *ToastModel) SetTheme(theme Theme) { t.theme = theme }

// Deleted announces a just-completed delete. When synced is true, the copy
// signals the server has already received the DELETE. When false, the local
// delete is still unpushed. Returns a command that fires the
// auto-dismiss tick.
func (t *ToastModel) Deleted(title string, synced bool) tea.Cmd {
	t.token++
	t.title = title
	t.reason = ""
	if synced {
		t.state = ToastDeletedSynced
	} else {
		t.state = ToastDeletedUnsynced
	}
	t.deadline = time.Now().Add(ToastAutoDismissDelay)
	return t.scheduleDismiss(ToastAutoDismissDelay)
}

// Restoring replaces the current toast copy with an in-flight indicator. It
// does not reset the dismiss timer. The caller will transition to Empty or
// Failed once the restore completes.
func (t *ToastModel) Restoring() {
	t.state = ToastRestoring
}

// Failed shows a short error message with a faster auto-dismiss. The
// message starts with "<action> failed:". The caller names the action, for
// example "Undo" or "Restore".
func (t *ToastModel) Failed(action, reason string) tea.Cmd {
	t.token++
	t.state = ToastFailed
	t.action = action
	t.reason = reason
	t.deadline = time.Now().Add(ToastFailedDismissDelay)
	return t.scheduleDismiss(ToastFailedDismissDelay)
}

// Restored confirms a successful undo. It replaces Restoring in-place (via
// token bump) and auto-dismisses after a short 3s window. By the time it
// shows, the user has already seen the row reappear in the grid.
func (t *ToastModel) Restored(title string) tea.Cmd {
	return t.RestoredCount(title, 1)
}

// RestoredCount confirms the restore of n items. For n above one, the
// toast shows the count, and it does not show title.
func (t *ToastModel) RestoredCount(title string, n int) tea.Cmd {
	t.token++
	t.state = ToastRestored
	t.title = title
	t.count = n
	t.reason = ""
	t.deadline = time.Now().Add(ToastRestoredDismissDelay)
	return t.scheduleDismiss(ToastRestoredDismissDelay)
}

// Purged confirms a successful hard-delete from the trash view. Uses the
// same short dismiss window as Restored — the row has already disappeared
// from the list by the time the toast shows.
func (t *ToastModel) Purged(title string) tea.Cmd {
	return t.PurgedCount(title, 1)
}

// PurgedCount confirms the purge of n items. For n above one, the toast
// shows the count, and it does not show title.
func (t *ToastModel) PurgedCount(title string, n int) tea.Cmd {
	t.token++
	t.state = ToastPurged
	t.title = title
	t.count = n
	t.reason = ""
	t.deadline = time.Now().Add(ToastRestoredDismissDelay)
	return t.scheduleDismiss(ToastRestoredDismissDelay)
}

// Clear hides the toast immediately and invalidates any tick that is still armed.
func (t *ToastModel) Clear() {
	t.token++
	t.state = ToastEmpty
	t.title = ""
	t.reason = ""
}

// Update handles tick messages. Returns true when the message was consumed
// so the host can skip further work on it.
func (t *ToastModel) Update(msg tea.Msg) bool {
	tick, ok := msg.(toastTickMsg)
	if !ok {
		return false
	}
	if tick.token != t.token {
		// A newer toast has superseded this one; ignore stale tick.
		return true
	}
	// Only auto-dismiss from the terminal display states. Restoring is a
	// transient indicator owned by the caller, not the timer.
	switch t.state {
	case ToastDeletedUnsynced, ToastDeletedSynced, ToastRestored, ToastFailed, ToastPurged:
		t.state = ToastEmpty
		t.title = ""
		t.reason = ""
	default:
		// Empty and Restoring are not timer-dismissed.
	}
	return true
}

// State exposes the current state (mainly for tests).
func (t ToastModel) State() ToastState { return t.state }

// IsVisible reports whether the toast has content to render.
func (t ToastModel) IsVisible() bool { return t.state != ToastEmpty }

// View renders the toast. The caller composites the result into the footer.
func (t ToastModel) View() string {
	if t.state == ToastEmpty {
		return ""
	}
	keyStyle := lipgloss.NewStyle().Foreground(t.theme.Text).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(t.theme.TextDim)
	okStyle := lipgloss.NewStyle().Foreground(t.theme.Accent)
	errStyle := lipgloss.NewStyle().Foreground(t.theme.Error)

	switch t.state {
	case ToastDeletedUnsynced:
		return fmt.Sprintf("%s %s — undo %s",
			okStyle.Render("✓"),
			dimStyle.Render(fmt.Sprintf("Deleted %q", t.title)),
			keyStyle.Render("("+t.undoKey+")"))
	case ToastDeletedSynced:
		return fmt.Sprintf("%s %s — undo %s",
			okStyle.Render("✓"),
			dimStyle.Render(fmt.Sprintf("Deleted %q (synced)", t.title)),
			keyStyle.Render("("+t.undoKey+")"))
	case ToastRestoring:
		return dimStyle.Render("Restoring…")
	case ToastRestored:
		label := "Restored event"
		switch {
		case t.count > 1:
			label = fmt.Sprintf("Restored %d items", t.count)
		case t.title != "":
			label = fmt.Sprintf("Restored %q", t.title)
		}
		return fmt.Sprintf("%s %s", okStyle.Render("✓"), dimStyle.Render(label))
	case ToastPurged:
		label := "Purged forever"
		switch {
		case t.count > 1:
			label = fmt.Sprintf("Purged %d items forever", t.count)
		case t.title != "":
			label = fmt.Sprintf("Purged %q forever", t.title)
		}
		return fmt.Sprintf("%s %s", okStyle.Render("✓"), dimStyle.Render(label))
	case ToastFailed:
		return fmt.Sprintf("%s %s",
			errStyle.Render("✗"),
			dimStyle.Render(t.action+" failed: "+t.reason))
	default:
		// ToastEmpty is short-circuited above; nothing else renders.
		return ""
	}
}

func (t ToastModel) scheduleDismiss(d time.Duration) tea.Cmd {
	token := t.token
	return tea.Tick(d, func(time.Time) tea.Msg {
		return toastTickMsg{token: token}
	})
}
