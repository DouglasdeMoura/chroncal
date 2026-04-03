package journal

import (
	"testing"
	"time"
)

func TestJournal_ParseStartDate(t *testing.T) {
	t.Parallel()
	t.Run("valid RFC3339", func(t *testing.T) {
		j := Journal{StartDate: "2026-04-01T09:00:00Z"}
		got := j.ParseStartDate()
		if got.IsZero() {
			t.Error("ParseStartDate() returned zero time for valid input")
		}
		if got.Day() != 1 || got.Month() != time.April {
			t.Errorf("ParseStartDate() = %v, want April 1", got)
		}
	})
	t.Run("valid date-only", func(t *testing.T) {
		j := Journal{StartDate: "2026-04-01"}
		got := j.ParseStartDate()
		if got.IsZero() {
			t.Error("ParseStartDate() returned zero time for date-only input")
		}
		if got.Day() != 1 || got.Month() != time.April || got.Year() != 2026 {
			t.Errorf("ParseStartDate() = %v, want 2026-04-01", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		j := Journal{StartDate: ""}
		got := j.ParseStartDate()
		if !got.IsZero() {
			t.Errorf("ParseStartDate() on empty = %v, want zero", got)
		}
	})
}

func TestJournal_ParseCategories(t *testing.T) {
	t.Parallel()
	j := Journal{Categories: "notes,personal,work"}
	got := j.ParseCategories()
	if len(got) != 3 {
		t.Errorf("ParseCategories() returned %d items, want 3", len(got))
	}
}
