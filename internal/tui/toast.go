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
	ToastDeletedUnsynced                   // "Deleted 'X' — undo (u)"
	ToastDeletedSynced                     // "Deleted 'X' (synced) — undo (u)"
	ToastRestoring                         // "Restoring…"
	ToastRestored                          // "✓ Restored 'X'"
	ToastFailed                            // "Undo failed: <reason>"
	ToastPurged                            // "✓ Purged 'X'"
)

// ToastAutoDismissDelay is the default 6-second window a deleted-toast lives
// before it fades. A failed-toast uses the shorter value below.
const ToastAutoDismissDelay = 6 * time.Second

// ToastFailedDismissDelay is the shorter window for an undo-failed toast —
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
// slot, last wins" is the whole mental model: a second delete within the 6s
// window replaces the first toast, and the undo stack (independent of the
// toast) still remembers both.
type ToastModel struct {
	state    ToastState
	title    string
	reason   string
	token    int
	theme    Theme
	deadline time.Time
}

// NewToastModel returns an empty toast bound to the given theme.
func NewToastModel(theme Theme) ToastModel {
	return ToastModel{theme: theme, state: ToastEmpty}
}

// Theme lets late theme switches propagate to the toast.
func (t *ToastModel) SetTheme(theme Theme) { t.theme = theme }

// Deleted announces a just-completed delete. When synced is true, the copy
// signals the server has already received the DELETE; when false, the local
// delete is still pending upload. Returns a command that fires the
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
// does not reset the dismiss timer — the caller will transition to Empty or
// Failed once the restore completes.
func (t *ToastModel) Restoring() {
	t.state = ToastRestoring
}

// Failed shows a short error message with a faster auto-dismiss.
func (t *ToastModel) Failed(reason string) tea.Cmd {
	t.token++
	t.state = ToastFailed
	t.reason = reason
	t.deadline = time.Now().Add(ToastFailedDismissDelay)
	return t.scheduleDismiss(ToastFailedDismissDelay)
}

// Restored confirms a successful undo. It replaces Restoring in-place (via
// token bump) and auto-dismisses after a short 3s window — by the time it
// shows, the user has already seen the row reappear in the grid.
func (t *ToastModel) Restored(title string) tea.Cmd {
	t.token++
	t.state = ToastRestored
	t.title = title
	t.reason = ""
	t.deadline = time.Now().Add(ToastRestoredDismissDelay)
	return t.scheduleDismiss(ToastRestoredDismissDelay)
}

// Purged confirms a successful hard-delete from the trash view. Uses the
// same short dismiss window as Restored — the row has already disappeared
// from the list by the time the toast shows.
func (t *ToastModel) Purged(title string) tea.Cmd {
	t.token++
	t.state = ToastPurged
	t.title = title
	t.reason = ""
	t.deadline = time.Now().Add(ToastRestoredDismissDelay)
	return t.scheduleDismiss(ToastRestoredDismissDelay)
}

// Clear hides the toast immediately and invalidates any pending tick.
func (t *ToastModel) Clear() {
	t.token++
	t.state = ToastEmpty
	t.title = ""
	t.reason = ""
}

// Update handles tick messages. Returns true when the message was consumed
// so the host can skip further handling.
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
	}
	return true
}

// State exposes the current state (mainly for tests).
func (t ToastModel) State() ToastState { return t.state }

// IsVisible reports whether the toast has anything to render.
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
			keyStyle.Render("(u)"))
	case ToastDeletedSynced:
		return fmt.Sprintf("%s %s — undo %s",
			okStyle.Render("✓"),
			dimStyle.Render(fmt.Sprintf("Deleted %q (synced)", t.title)),
			keyStyle.Render("(u)"))
	case ToastRestoring:
		return dimStyle.Render("Restoring…")
	case ToastRestored:
		label := "Restored event"
		if t.title != "" {
			label = fmt.Sprintf("Restored %q", t.title)
		}
		return fmt.Sprintf("%s %s", okStyle.Render("✓"), dimStyle.Render(label))
	case ToastPurged:
		label := "Purged forever"
		if t.title != "" {
			label = fmt.Sprintf("Purged %q forever", t.title)
		}
		return fmt.Sprintf("%s %s", okStyle.Render("✓"), dimStyle.Render(label))
	case ToastFailed:
		return fmt.Sprintf("%s %s",
			errStyle.Render("✗"),
			dimStyle.Render("Undo failed: "+t.reason))
	}
	return ""
}

func (t ToastModel) scheduleDismiss(d time.Duration) tea.Cmd {
	token := t.token
	return tea.Tick(d, func(time.Time) tea.Msg {
		return toastTickMsg{token: token}
	})
}
