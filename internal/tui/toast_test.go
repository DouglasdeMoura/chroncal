package tui

import (
	"strings"
	"testing"
)

func TestToast_DeletedShowsKey(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Deleted("Standup", false)
	v := m.View()
	if !strings.Contains(v, "Standup") {
		t.Errorf("View = %q, missing title", v)
	}
	if !strings.Contains(v, "(u)") {
		t.Errorf("View = %q, missing undo key", v)
	}
	if m.State() != ToastDeletedUnsynced {
		t.Errorf("State = %v, want ToastDeletedUnsynced", m.State())
	}
}

func TestToast_DeletedSyncedDiffers(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Deleted("X", true)
	if !strings.Contains(m.View(), "synced") {
		t.Errorf("View = %q, want 'synced' marker", m.View())
	}
}

func TestToast_LastWinsReplacement(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Deleted("First", false)
	tok1 := m.token
	m.Deleted("Second", false)
	tok2 := m.token
	if tok1 == tok2 {
		t.Fatal("token did not change on replacement")
	}
	if !strings.Contains(m.View(), "Second") {
		t.Errorf("View = %q, want latest title 'Second'", m.View())
	}
}

func TestToast_StaleTickIgnored(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Deleted("First", false)
	staleTok := m.token
	m.Deleted("Second", false) // bumps token
	consumed := m.Update(toastTickMsg{token: staleTok})
	if !consumed {
		t.Fatal("Update should report consumed=true even for stale tick")
	}
	if m.State() != ToastDeletedUnsynced {
		t.Errorf("Stale tick dismissed the live toast; state = %v", m.State())
	}
}

func TestToast_FreshTickDismisses(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Deleted("X", false)
	m.Update(toastTickMsg{token: m.token})
	if m.State() != ToastEmpty {
		t.Errorf("State after fresh tick = %v, want ToastEmpty", m.State())
	}
	if m.IsVisible() {
		t.Error("IsVisible = true after dismiss")
	}
}

func TestToast_RestoringDoesNotAutoDismiss(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Deleted("X", false)
	m.Restoring()
	m.Update(toastTickMsg{token: m.token})
	// After the tick fires, state should drop to Empty (Restoring isn't
	// listed among auto-dismiss states, so it sticks).
	if m.State() != ToastRestoring {
		t.Errorf("State = %v, want ToastRestoring (tick should not dismiss it)", m.State())
	}
}

func TestToast_FailedState(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Failed("calendar deleted")
	if !strings.Contains(m.View(), "calendar deleted") {
		t.Errorf("View = %q, missing reason", m.View())
	}
	if !strings.Contains(m.View(), "Undo failed") {
		t.Errorf("View = %q, missing 'Undo failed' prefix", m.View())
	}
}

func TestToast_Clear(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Deleted("X", false)
	m.Clear()
	if m.IsVisible() {
		t.Error("Clear did not hide toast")
	}
}

func TestToast_RestoredShowsTitle(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Restored("Standup")
	if m.State() != ToastRestored {
		t.Errorf("State = %v, want ToastRestored", m.State())
	}
	v := m.View()
	if !strings.Contains(v, "Restored") {
		t.Errorf("View = %q, missing 'Restored'", v)
	}
	if !strings.Contains(v, "Standup") {
		t.Errorf("View = %q, missing title echo", v)
	}
}

func TestToast_RestoredEmptyTitleFallback(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Restored("")
	if !strings.Contains(m.View(), "Restored event") {
		t.Errorf("View = %q, want 'Restored event' fallback", m.View())
	}
}

func TestToast_RestoredAutoDismisses(t *testing.T) {
	m := NewToastModel(NewTheme(true))
	m.Restored("X")
	m.Update(toastTickMsg{token: m.token})
	if m.State() != ToastEmpty {
		t.Errorf("State after fresh tick = %v, want ToastEmpty", m.State())
	}
}

func TestToast_DeletedSupersedesRestored(t *testing.T) {
	// Rapid cycle: restore followed by another delete. The new toast must
	// win via token bump, so the stale Restored tick cannot clear it.
	m := NewToastModel(NewTheme(true))
	m.Restored("A")
	staleTok := m.token
	m.Deleted("B", false)
	consumed := m.Update(toastTickMsg{token: staleTok})
	if !consumed {
		t.Fatal("stale tick should still be consumed")
	}
	if m.State() != ToastDeletedUnsynced {
		t.Errorf("State = %v, want ToastDeletedUnsynced (latest)", m.State())
	}
}
