package calendar

import (
	"context"
	"testing"
)

func TestApplyCreatesMissingCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Apply(ctx, []Declared{
		{Name: "Fastmail", Path: "/home/me/calendars/fastmail"},
		{Name: "Work", Color: "#0F766E", Description: "Office"},
	})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}

	cals, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	byName := map[string]Calendar{}
	for _, c := range cals {
		byName[c.Name] = c
	}
	// The seed calendar stays. Apply never deletes.
	if _, ok := byName["Personal"]; !ok {
		t.Fatalf("Apply removed the seed calendar: %v", cals)
	}
	fm := byName["Fastmail"]
	if fm.ID == 0 || fm.LocalPath != "/home/me/calendars/fastmail" {
		t.Fatalf("Fastmail = %+v, want a created calendar with the declared path", fm)
	}
	if fm.Color != DefaultColor {
		t.Errorf("Fastmail color = %q, want the default %q", fm.Color, DefaultColor)
	}
	work := byName["Work"]
	if work.Color != "#0F766E" || work.Description != "Office" || work.LocalPath != "" {
		t.Fatalf("Work = %+v, want declared color and description, no path", work)
	}
}

func TestApplyUpdatesOnlyDeclaredFields(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	cal, err := svc.Create(ctx, "Fastmail", "#111111", "hand-written")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if err := svc.SetLocalPath(ctx, cal.ID, "/old/path"); err != nil {
		t.Fatalf("SetLocalPath error: %v", err)
	}

	// An entry with a name only touches nothing.
	if err := svc.Apply(ctx, []Declared{{Name: "Fastmail"}}); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	got, err := svc.Get(ctx, cal.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Color != "#111111" || got.Description != "hand-written" || got.LocalPath != "/old/path" {
		t.Fatalf("name-only entry changed the calendar: %+v", got)
	}

	// A declared color and path win over the stored values. The
	// description stays because the entry does not set it.
	if err := svc.Apply(ctx, []Declared{{Name: "Fastmail", Color: "#222222", Path: "/new/path"}}); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	got, err = svc.Get(ctx, cal.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Color != "#222222" || got.LocalPath != "/new/path" {
		t.Fatalf("Apply did not update color and path: %+v", got)
	}
	if got.Description != "hand-written" {
		t.Errorf("Apply reset the description: %q", got.Description)
	}
	if got.ID != cal.ID {
		t.Errorf("Apply replaced the calendar row: id %d, want %d", got.ID, cal.ID)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	declared := []Declared{{Name: "Fastmail", Color: "#222222", Path: "/p"}}

	for i := 0; i < 3; i++ {
		if err := svc.Apply(ctx, declared); err != nil {
			t.Fatalf("Apply #%d error: %v", i, err)
		}
	}
	cals, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(cals) != 2 {
		t.Fatalf("List returned %d calendars after three applies, want 2 (seed + Fastmail): %v", len(cals), cals)
	}
}

func TestApplyRejectsEmptyName(t *testing.T) {
	svc := newTestService(t)
	if err := svc.Apply(context.Background(), []Declared{{Name: "  "}}); err == nil {
		t.Fatal("Apply accepted an entry without a name")
	}
}

func TestApplyEmptyListIsNoop(t *testing.T) {
	svc := newTestService(t)
	if err := svc.Apply(context.Background(), nil); err != nil {
		t.Fatalf("Apply(nil) error: %v", err)
	}
}
