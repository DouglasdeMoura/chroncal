package calendar

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

func TestApplyCreatesMissingCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Apply(ctx, []Declared{
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
	if _, err := svc.Apply(ctx, []Declared{{Name: "Fastmail"}}); err != nil {
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
	if _, err := svc.Apply(ctx, []Declared{{Name: "Fastmail", Color: "#222222", Path: "/new/path"}}); err != nil {
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
		if _, err := svc.Apply(ctx, declared); err != nil {
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
	if _, err := svc.Apply(context.Background(), []Declared{{Name: "  "}}); err == nil {
		t.Fatal("Apply accepted an entry without a name")
	}
}

func TestApplyEmptyListIsNoop(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.Apply(context.Background(), nil); err != nil {
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

	warnings, err := svc.Apply(ctx, []Declared{{Name: "Personal", Color: "#222222", Path: "/p"}})
	if err != nil {
		t.Fatalf("Apply on a remote calendar must warn, not fail: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], ErrDeclaredRemote.Error()) {
		t.Fatalf("Apply warnings = %v, want one ErrDeclaredRemote warning", warnings)
	}
	// The skipped entry does not bind the guard, so the calendar stays
	// free to rename.
	if _, err := svc.Update(ctx, 1, "Mine", "#111111", ""); err != nil {
		t.Fatalf("rename of a skipped remote calendar: %v", err)
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
	if _, err := svc.Apply(ctx, []Declared{{Name: "Fastmail"}}); err != nil {
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

func TestApplyMatchesNameWithoutCase(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	stored, err := svc.Create(ctx, "Fastmail", "#111111", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Apply(ctx, []Declared{{Name: "fastmail", Path: "/p"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	cals, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cals) != 2 {
		t.Fatalf("Apply created a second calendar for a case variant: %v", cals)
	}
	got, err := svc.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Fastmail" || got.LocalPath != "/p" {
		t.Fatalf("stored calendar after Apply = %+v, want the kept name and the declared path", got)
	}
	// The declared guard matches the stored spelling too.
	if err := svc.Delete(ctx, stored.ID); !errors.Is(err, ErrDeclaredCalendar) {
		t.Fatalf("delete of a case-variant declared calendar: error = %v", err)
	}
}

func TestSetLocalPathRefusesRemoteCalendar(t *testing.T) {
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
	if err := svc.SetLocalPath(ctx, 1, "/p"); !errors.Is(err, ErrRemoteLocalPath) {
		t.Fatalf("SetLocalPath on a remote calendar: error = %v, want ErrRemoteLocalPath", err)
	}
	// Removal of a path is always allowed.
	if err := svc.SetLocalPath(ctx, 1, ""); err != nil {
		t.Fatalf("SetLocalPath(\"\") on a remote calendar: %v", err)
	}
}

func TestApplyWarnsOnAmbiguousCaseVariants(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"Work", "work"} {
		if _, err := svc.Create(ctx, name, "#111111", ""); err != nil {
			t.Fatalf("Create %q: %v", name, err)
		}
	}
	warnings, err := svc.Apply(ctx, []Declared{{Name: "WORK", Path: "/p"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], ErrAmbiguousName.Error()) {
		t.Fatalf("warnings = %v, want one ambiguity warning", warnings)
	}
	cals, _ := svc.List(ctx)
	if len(cals) != 3 {
		t.Fatalf("Apply created a calendar for an ambiguous entry: %v", cals)
	}
	for _, c := range cals {
		if c.LocalPath != "" {
			t.Fatalf("Apply set a path on an ambiguous match: %+v", c)
		}
		if c.Name != "Personal" {
			// Neither variant is bound, so both stay free to delete.
			if err := svc.Delete(ctx, c.ID); err != nil {
				t.Fatalf("delete of an unbound variant %q: %v", c.Name, err)
			}
		}
	}
}

func TestApplyDoesNotAdoptRemoteCaseVariant(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "test", ServerUrl: "https://example.com", AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// The seed calendar "Personal" becomes a remote calendar.
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: 1, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("https://example.com/cal/personal"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	warnings, err := svc.Apply(ctx, []Declared{{Name: "personal", Path: "/p"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("a remote case variant must not block the entry: %v", warnings)
	}
	got, err := svc.getByName(ctx, "personal")
	if err != nil || got.Name != "personal" || got.AccountID != 0 || got.LocalPath != "/p" {
		t.Fatalf("Apply did not create the local calendar: %+v, %v", got, err)
	}
}

func TestSetLocalPathRefusesOverlap(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateWithPath(ctx, "Parent", "#111111", "", "/srv/cal"); err != nil {
		t.Fatalf("CreateWithPath: %v", err)
	}
	child, err := svc.Create(ctx, "Child", "#111111", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, path := range []string{"/srv/cal", "/srv/cal/work", "/srv"} {
		if err := svc.SetLocalPath(ctx, child.ID, path); !errors.Is(err, ErrOverlappingPath) {
			t.Errorf("SetLocalPath(%q) error = %v, want ErrOverlappingPath", path, err)
		}
	}
	if err := svc.SetLocalPath(ctx, child.ID, "/srv/calendar"); err != nil {
		t.Fatalf("SetLocalPath on a sibling with a shared prefix: %v", err)
	}
	if _, err := svc.CreateWithPath(ctx, "Twin", "#111111", "", "/srv/cal/other"); !errors.Is(err, ErrOverlappingPath) {
		t.Fatalf("CreateWithPath inside another tree: error = %v, want ErrOverlappingPath", err)
	}
}

func TestApplyWarnsWhenItResetsAStoredField(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	cal, err := svc.CreateWithPath(ctx, "Fastmail", "#111111", "", "/old")
	if err != nil {
		t.Fatalf("CreateWithPath: %v", err)
	}
	warnings, err := svc.Apply(ctx, []Declared{{Name: "Fastmail", Color: "#222222", Path: "/new"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The color resets in the first pass and the path in the second, so
	// two warnings name the two fields.
	if len(warnings) != 2 || !strings.Contains(warnings[0], "replaced the stored color") || !strings.Contains(warnings[1], "replaced the stored path") {
		t.Fatalf("warnings = %v, want a color reset warning and a path reset warning", warnings)
	}
	got, _ := svc.Get(ctx, cal.ID)
	if got.Color != "#222222" || got.LocalPath != "/new" {
		t.Fatalf("Apply did not apply the entry: %+v", got)
	}
	// The second apply changes nothing and warns about nothing.
	warnings, err = svc.Apply(ctx, []Declared{{Name: "Fastmail", Color: "#222222", Path: "/new"}})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("second Apply: warnings = %v, err = %v", warnings, err)
	}
}

func TestInputErrorsCarryTheMarker(t *testing.T) {
	for _, err := range []error{ErrDuplicateName, ErrDeclaredCalendar, ErrDeclaredRemote, ErrRemoteLocalPath, ErrLocalPathLink, ErrAmbiguousName, ErrOverlappingPath} {
		var input InputError
		if !errors.As(err, &input) || !input.InvalidInput() {
			t.Errorf("%v does not carry the InputError marker", err)
		}
	}
	var input InputError
	if errors.As(ErrLastCalendar, &input) {
		t.Error("ErrLastCalendar must not carry the InputError marker")
	}
}

func TestApplyConvergesOnAPathSwap(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateWithPath(ctx, "A", "#111111", "", "/p1"); err != nil {
		t.Fatalf("create A: %v", err)
	}
	if _, err := svc.CreateWithPath(ctx, "B", "#111111", "", "/p2"); err != nil {
		t.Fatalf("create B: %v", err)
	}
	warnings, err := svc.Apply(ctx, []Declared{{Name: "A", Path: "/p2"}, {Name: "B", Path: "/p1"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	a, _ := svc.getByName(ctx, "A")
	b, _ := svc.getByName(ctx, "B")
	if a.LocalPath != "/p2" || b.LocalPath != "/p1" {
		t.Fatalf("paths after swap: A=%q B=%q, warnings=%v", a.LocalPath, b.LocalPath, warnings)
	}
	for _, w := range warnings {
		if strings.Contains(w, ErrOverlappingPath.Error()) {
			t.Fatalf("a swap must not warn about an overlap: %v", warnings)
		}
	}
}

func TestApplyCreatesWithoutAnOverlappingPath(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateWithPath(ctx, "Old", "#111111", "", "/srv/cal"); err != nil {
		t.Fatalf("create Old: %v", err)
	}
	warnings, err := svc.Apply(ctx, []Declared{{Name: "New", Path: "/srv/cal/work"}})
	if err != nil {
		t.Fatalf("Apply must warn, not fail, on an overlap: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], ErrOverlappingPath.Error()) {
		t.Fatalf("warnings = %v, want one overlap warning", warnings)
	}
	got, err := svc.getByName(ctx, "New")
	if err != nil || got.LocalPath != "" {
		t.Fatalf("New = %+v, %v; want a calendar without a path", got, err)
	}
	if !svc.IsDeclared("New") {
		t.Fatal("the created calendar must still be bound to its entry")
	}
}
