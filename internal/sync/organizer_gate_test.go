package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// A missing master row means "no organizer known". The gate must answer
// true so an orphaned dirty row can still push.
func TestUserOrganizesEvent_MissingRowIsNoOrganizer(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	e := NewEngine(db, q, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	got, err := e.userOrganizesEvent(ctx, "no-such-uid", "owner@example.com")
	if err != nil {
		t.Fatalf("userOrganizesEvent: %v", err)
	}
	if !got {
		t.Errorf("userOrganizesEvent = false for a missing row, want true")
	}
}

// Any lookup failure other than ErrNoRows must surface as an error. The
// caller keeps the row dirty; the gate must not guess in either direction.
func TestUserOrganizesEvent_LookupFailurePropagates(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	e := NewEngine(db, q, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	_, err := e.userOrganizesEvent(ctx, "some-uid", "owner@example.com")
	if err == nil {
		t.Fatal("userOrganizesEvent returned no error on a failed lookup, want error")
	}
	if errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("unexpected sentinel: %v", err)
	}
}
