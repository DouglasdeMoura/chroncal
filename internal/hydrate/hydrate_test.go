package hydrate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/hydrate"
)

var errBoom = errors.New("boom")

// loader returns a load function that records whether it was called and the
// ID it was called with, then yields the given values.
func loader[T any](called *bool, gotID *int64, v []T, err error) func(context.Context, int64) ([]T, error) {
	return func(_ context.Context, id int64) ([]T, error) {
		*called = true
		if gotID != nil {
			*gotID = id
		}
		return v, err
	}
}

func TestRelAssignsAndErrNilWhenClean(t *testing.T) {
	ctx := context.Background()
	c := &hydrate.Collector{Kind: "event", ID: 7}

	var dst []string
	var called bool
	var gotID int64
	hydrate.Rel(ctx, c, &dst, "comments", loader(&called, &gotID, []string{"a", "b"}, nil))

	if !called {
		t.Fatal("loader was not called")
	}
	if gotID != 7 {
		t.Fatalf("loader called with ID %d, want 7", gotID)
	}
	if len(dst) != 2 || dst[0] != "a" || dst[1] != "b" {
		t.Fatalf("dst = %v, want [a b]", dst)
	}
	if err := c.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
}

func TestRelErrorFormat(t *testing.T) {
	ctx := context.Background()
	c := &hydrate.Collector{Kind: "event", ID: 42}

	var dst []int
	var called bool
	hydrate.Rel(ctx, c, &dst, "alarms", loader[int](&called, nil, nil, errBoom))

	err := c.Err()
	if err == nil {
		t.Fatal("Err() = nil, want error")
	}
	if got, want := err.Error(), "event 42 alarms: boom"; got != want {
		t.Fatalf("Err() = %q, want %q", got, want)
	}
	if !errors.Is(err, errBoom) {
		t.Fatal("Err() does not unwrap to the loader's cause")
	}
}

func TestRelFailFastStopsLoading(t *testing.T) {
	ctx := context.Background()
	c := &hydrate.Collector{Kind: "todo", ID: 3, FailFast: true}

	var alarms, attendees []string
	var firstCalled, secondCalled bool
	hydrate.Rel(ctx, c, &alarms, "alarms", loader[string](&firstCalled, nil, nil, errBoom))
	hydrate.Rel(ctx, c, &attendees, "attendees", loader(&secondCalled, nil, []string{"x"}, nil))

	if !firstCalled {
		t.Fatal("first loader was not called")
	}
	if secondCalled {
		t.Fatal("fail-fast collector still called a loader after the first error")
	}
	if attendees != nil {
		t.Fatalf("attendees = %v, want nil (must stay untouched after stop)", attendees)
	}
	err := c.Err()
	if err == nil {
		t.Fatal("Err() = nil, want error")
	}
	if got, want := err.Error(), "todo 3 alarms: boom"; got != want {
		t.Fatalf("Err() = %q, want %q", got, want)
	}
}

func TestRelBestEffortContinuesAndJoins(t *testing.T) {
	ctx := context.Background()
	c := &hydrate.Collector{Kind: "journal", ID: 9}

	errOther := errors.New("nope")
	var attendees, comments, contacts []string
	var c1, c2, c3 bool
	hydrate.Rel(ctx, c, &attendees, "attendees", loader[string](&c1, nil, nil, errBoom))
	hydrate.Rel(ctx, c, &comments, "comments", loader(&c2, nil, []string{"ok"}, nil))
	hydrate.Rel(ctx, c, &contacts, "contacts", loader[string](&c3, nil, nil, errOther))

	if !c1 || !c2 || !c3 {
		t.Fatalf("loaders called = %v %v %v, want all true", c1, c2, c3)
	}
	if len(comments) != 1 || comments[0] != "ok" {
		t.Fatalf("comments = %v, want [ok] (successful load must still assign)", comments)
	}
	if attendees != nil {
		t.Fatalf("attendees = %v, want nil (failed load must not assign)", attendees)
	}

	err := c.Err()
	if !errors.Is(err, errBoom) || !errors.Is(err, errOther) {
		t.Fatalf("Err() = %v, want both causes joined", err)
	}
	want := "journal 9 attendees: boom\njournal 9 contacts: nope"
	if got := err.Error(); got != want {
		t.Fatalf("Err() = %q, want %q", got, want)
	}
}
