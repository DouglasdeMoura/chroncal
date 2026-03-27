package todo

import (
	"testing"
	"time"
)

func TestTodo_IsCompleted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status string
		want   bool
	}{
		{"COMPLETED", true},
		{"NEEDS-ACTION", false},
		{"IN-PROCESS", false},
		{"CANCELLED", false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			todo := Todo{Status: tt.status}
			if got := todo.IsCompleted(); got != tt.want {
				t.Errorf("IsCompleted() with status %q = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestTodo_IsOverdue(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		name    string
		dueDate string
		status  string
		want    bool
	}{
		{"past due, incomplete", past, "NEEDS-ACTION", true},
		{"future due, incomplete", future, "NEEDS-ACTION", false},
		{"past due, completed", past, "COMPLETED", false},
		{"no due date", "", "NEEDS-ACTION", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			todo := Todo{DueDate: tt.dueDate, Status: tt.status}
			if got := todo.IsOverdue(); got != tt.want {
				t.Errorf("IsOverdue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTodo_ParseDueDate(t *testing.T) {
	t.Parallel()
	t.Run("valid", func(t *testing.T) {
		todo := Todo{DueDate: "2026-04-01T23:59:59Z"}
		got := todo.ParseDueDate()
		if got.IsZero() {
			t.Error("ParseDueDate() returned zero time for valid input")
		}
		if got.Day() != 1 || got.Month() != time.April {
			t.Errorf("ParseDueDate() = %v, want April 1", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		todo := Todo{DueDate: ""}
		got := todo.ParseDueDate()
		if !got.IsZero() {
			t.Errorf("ParseDueDate() on empty = %v, want zero", got)
		}
	})
}

func TestTodo_ParseStartDate(t *testing.T) {
	t.Parallel()
	t.Run("valid", func(t *testing.T) {
		todo := Todo{StartDate: "2026-04-01T09:00:00Z"}
		got := todo.ParseStartDate()
		if got.IsZero() {
			t.Error("ParseStartDate() returned zero time")
		}
	})
	t.Run("empty", func(t *testing.T) {
		todo := Todo{StartDate: ""}
		got := todo.ParseStartDate()
		if !got.IsZero() {
			t.Errorf("ParseStartDate() on empty = %v, want zero", got)
		}
	})
}

func TestTodo_ParseCompletedAt(t *testing.T) {
	t.Parallel()
	t.Run("valid", func(t *testing.T) {
		todo := Todo{CompletedAt: "2026-04-01T10:00:00Z"}
		got := todo.ParseCompletedAt()
		if got.IsZero() {
			t.Error("ParseCompletedAt() returned zero time")
		}
	})
	t.Run("empty", func(t *testing.T) {
		todo := Todo{CompletedAt: ""}
		got := todo.ParseCompletedAt()
		if !got.IsZero() {
			t.Errorf("ParseCompletedAt() on empty = %v, want zero", got)
		}
	})
}

func TestTodo_ParseCategories(t *testing.T) {
	t.Parallel()
	todo := Todo{Categories: "work,dev,urgent"}
	got := todo.ParseCategories()
	if len(got) != 3 {
		t.Errorf("ParseCategories() returned %d items, want 3", len(got))
	}
}
