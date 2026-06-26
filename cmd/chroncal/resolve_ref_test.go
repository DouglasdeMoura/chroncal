package main

import (
	"context"
	"errors"
	"testing"
)

// When a reference is a numeric ID, the master row is resolved directly and a
// non-empty --recurrence-id cannot be honored (IDs are unique per row, so the
// override would have to be addressed by its own ID). Silently dropping the
// recurrence-id let `event delete 42 --recurrence-id ...` render an
// override-specific confirm prompt and then soft-delete the master. Reject the
// combination instead. See issue #114.
func TestResolveRefNumericWithRecurrenceIDRejected(t *testing.T) {
	idCalled := false
	getByID := func(context.Context, int64) (int, error) {
		idCalled = true
		return 42, nil
	}
	getByUID := func(context.Context, string) (int, error) {
		t.Fatalf("getByUID should not be called for a numeric ref")
		return 0, nil
	}
	getByUIDAndRecurrenceID := func(context.Context, string, string) (int, error) {
		t.Fatalf("getByUIDAndRecurrenceID should not be called for a numeric ref")
		return 0, nil
	}

	_, err := resolveRef(context.Background(), "42", "2026-04-07T12:00:00Z", "event",
		getByID, getByUID, getByUIDAndRecurrenceID)
	if err == nil {
		t.Fatalf("expected error for numeric ref + recurrence-id, got nil")
	}
	if idCalled {
		t.Fatalf("getByID must not resolve the master when recurrence-id is set")
	}
	var ce *cliError
	if !errors.As(err, &ce) || ce.Code != "invalid_input" {
		t.Fatalf("expected invalid_input cliError, got %#v", err)
	}
}

// A numeric ID with no recurrence-id still resolves the master row directly.
func TestResolveRefNumericWithoutRecurrenceID(t *testing.T) {
	getByID := func(context.Context, int64) (int, error) { return 42, nil }
	fail2 := func(context.Context, string) (int, error) {
		t.Fatalf("getByUID should not be called for a numeric ref")
		return 0, nil
	}
	fail3 := func(context.Context, string, string) (int, error) {
		t.Fatalf("getByUIDAndRecurrenceID should not be called for a numeric ref")
		return 0, nil
	}

	got, err := resolveRef(context.Background(), "42", "", "event", getByID, fail2, fail3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
}
