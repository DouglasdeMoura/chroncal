package calendar

import (
	"context"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
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

func TestApplyRefusesRemoteCalendar(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "test", ServerUrl: "https://example.com", AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: 1, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("https://example.com/cal/personal"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	err = svc.Apply(ctx, []Declared{{Name: "Personal", Color: "#222222", Path: "/p"}})
	if !errors.Is(err, ErrDeclaredRemote) {
		t.Fatalf("Apply on a remote calendar: error = %v, want ErrDeclaredRemote", err)
	}
	got, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Color == "#222222" || got.LocalPath != "" {
		t.Fatalf("Apply changed the remote calendar: %+v", got)
	}
}

func TestApplyThenRenameAndDeleteRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if err := svc.Apply(ctx, []Declared{{Name: "Fastmail"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	cals, _ := svc.List(ctx)
	var fm Calendar
	for _, c := range cals {
		if c.Name == "Fastmail" {
			fm = c
		}
	}

	if _, err := svc.Update(ctx, fm.ID, "Mail", fm.Color, fm.Description); !errors.Is(err, ErrDeclaredCalendar) {
		t.Fatalf("rename of a declared calendar: error = %v, want ErrDeclaredCalendar", err)
	}
	// An update that keeps the name still works.
	if _, err := svc.Update(ctx, fm.ID, "Fastmail", "#010101", "note"); err != nil {
		t.Fatalf("update of a declared calendar without a rename: %v", err)
	}
	if err := svc.Delete(ctx, fm.ID); !errors.Is(err, ErrDeclaredCalendar) {
		t.Fatalf("delete of a declared calendar: error = %v, want ErrDeclaredCalendar", err)
	}
	if err := svc.DeleteAndPromote(ctx, fm.ID, 1); !errors.Is(err, ErrDeclaredCalendar) {
		t.Fatalf("DeleteAndPromote of a declared calendar: error = %v, want ErrDeclaredCalendar", err)
	}
	// The seed calendar is not declared, so its delete still works.
	if err := svc.DeleteAndPromote(ctx, 1, fm.ID); err != nil {
		t.Fatalf("delete of an undeclared calendar: %v", err)
	}
}

func TestCreateWithPathStoresPathInOneTransaction(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	c, err := svc.CreateWithPath(ctx, "Fastmail", "#111111", "", "/srv/fastmail")
	if err != nil {
		t.Fatalf("CreateWithPath: %v", err)
	}
	if c.LocalPath != "/srv/fastmail" {
		t.Fatalf("returned LocalPath = %q", c.LocalPath)
	}
	got, err := svc.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LocalPath != "/srv/fastmail" {
		t.Fatalf("stored LocalPath = %q", got.LocalPath)
	}
}
