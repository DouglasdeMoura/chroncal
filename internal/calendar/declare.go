package calendar

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/fsutil"
)

// DefaultColor is the color of a new calendar when the caller gives none.
// The CLI --color flag, the account discovery, and the config
// [[calendars]] entries share it.
const DefaultColor = "#7C3AED"

// Declared is one [[calendars]] entry from config.toml (issue #809). Name
// is the key. An empty Color, Description, or Path leaves the stored value
// of that field as it is, so a short entry does not reset a field that the
// user set from the CLI or the TUI.
type Declared struct {
	Name        string
	Color       string
	Description string
	Path        string
}

// InputError marks a service error that reports a request the user must
// change, not a system failure. The CLI gives such an error the
// "invalid_input" code. The marker lives on the error, so a new sentinel
// needs no entry in a list elsewhere.
type InputError interface {
	error
	InvalidInput() bool
}

type inputError struct{ msg string }

func (e *inputError) Error() string      { return e.msg }
func (e *inputError) InvalidInput() bool { return true }

// newInputError returns a sentinel that satisfies InputError.
func newInputError(msg string) error { return &inputError{msg: msg} }

// ErrDeclaredCalendar reports a rename, a delete, a link, or a move of a
// calendar that a [[calendars]] entry of config.toml declares. The entry
// binds the calendar to its name and keeps it local. The user removes the
// entry first.
var ErrDeclaredCalendar = newInputError(
	"a [[calendars]] entry in config.toml declares this calendar, so it keeps its name and stays local; remove the entry first")

// ErrDeclaredRemote reports a [[calendars]] entry whose name matches a
// calendar that a CalDAV account owns. The table declares local calendars
// only. An apply would push the declared color to the server and feed the
// remote calendar from a local directory.
var ErrDeclaredRemote = newInputError(
	"a CalDAV account owns a calendar with this name; the [[calendars]] table declares local calendars only")

// ErrRemoteLocalPath reports a local directory on a calendar that a CalDAV
// account owns. An import from the directory would push the files to the
// server, which already owns the collection.
var ErrRemoteLocalPath = newInputError(
	"a CalDAV account owns this calendar, so it cannot have a local directory")

// ErrLocalPathLink reports a link of a calendar that has a local directory
// to a CalDAV account. The user removes the directory first. The text
// names no CLI flag, because the TUI reports the same error.
var ErrLocalPathLink = newInputError(
	"this calendar has a local directory; remove the directory before you link it to a CalDAV account")

// ErrAmbiguousName reports a name that matches several stored calendars
// that differ only in case and none exactly. Apply skips such an entry
// with a warning, and the CLI asks for the numeric ID. The user renames
// one of the calendars.
var ErrAmbiguousName = newInputError(
	"several calendars match this name in another case; rename one of them or use the numeric ID")

// ErrOverlappingPath reports a local directory that another calendar
// already uses, or that lies inside or above the directory of another
// calendar. The import reads a directory and its subdirectories, so two
// calendars on one tree would import the same files and move each todo
// and journal between them on every run.
var ErrOverlappingPath = newInputError(
	"another calendar uses this directory or a directory above or below it; each calendar needs its own tree")

// checkPathFree returns ErrOverlappingPath when another calendar than id
// uses path or a directory above or below it.
func checkPathFree(cals []Calendar, id int64, path string) error {
	for _, c := range cals {
		if c.ID != id && fsutil.SameTree(c.LocalPath, path) {
			return fmt.Errorf("calendar %q: %w", c.Name, ErrOverlappingPath)
		}
	}
	return nil
}

// IsDeclared reports whether Apply bound an entry to a stored calendar with
// that exact name. Apply records the stored spelling, so a stray case
// variant of a declared calendar stays free to delete or rename. The
// account service reads it through account.Service.SetDeclaredGuard, so a
// calendar move and a discovery relink respect the same set.
func (s *Service) IsDeclared(name string) bool {
	return s.declared[name]
}

// refuseDeclared returns ErrDeclaredCalendar when Apply bound an entry to
// a calendar with that name.
func (s *Service) refuseDeclared(name string) error {
	if s.IsDeclared(name) {
		return fmt.Errorf("calendar %q: %w", name, ErrDeclaredCalendar)
	}
	return nil
}

// CheckLinkable reports whether a calendar can take a CalDAV link. A
// declared calendar stays local (ErrDeclaredCalendar), and a calendar with a
// local directory cannot also be a server collection (ErrLocalPathLink).
// ConnectTarget and Connect call it, and the CLI calls it before it writes
// any other field of the calendar, so a refusal costs no prompt, no network
// request, and no partial update.
func (s *Service) CheckLinkable(cal Calendar) error {
	if err := s.refuseDeclared(cal.Name); err != nil {
		return err
	}
	if cal.LocalPath != "" {
		return fmt.Errorf("calendar %q: %w", cal.Name, ErrLocalPathLink)
	}
	return nil
}

// FindByName returns the calendar with that name. An exact match wins. One
// match that differs only in case comes next, so a declared "fastmail"
// adopts a stored "Fastmail" instead of a second calendar. The CLI
// --calendar lookup and Apply share this rule, so a name resolves the same
// way everywhere. Several case variants and no exact match give
// ErrAmbiguousName.
func FindByName(cals []Calendar, name string) (Calendar, bool, error) {
	for _, c := range cals {
		if c.Name == name {
			return c, true, nil
		}
	}
	var (
		found Calendar
		count int
	)
	for _, c := range cals {
		if strings.EqualFold(c.Name, name) {
			found = c
			count++
		}
	}
	switch count {
	case 0:
		return Calendar{}, false, nil
	case 1:
		return found, true, nil
	default:
		return Calendar{}, false, ErrAmbiguousName
	}
}

// getByName returns the calendar with that name (see FindByName).
func (s *Service) getByName(ctx context.Context, name string) (Calendar, error) {
	cals, err := s.List(ctx)
	if err != nil {
		return Calendar{}, err
	}
	c, ok, err := FindByName(cals, name)
	if err != nil {
		return Calendar{}, err
	}
	if !ok {
		return Calendar{}, fmt.Errorf("calendar %q not found", name)
	}
	return c, nil
}

// Apply reconciles the declared calendars into the database. For each
// entry it creates a missing calendar, and it updates a stored color,
// description, or path that differs from a non-empty declared value. It
// never deletes a calendar: an entry that goes away from the config leaves
// the calendar and its events in place. After Apply, Update refuses to
// rename a declared calendar, the delete methods refuse to delete one, and
// Connect refuses to link one to a CalDAV account.
//
// A conflict in one entry does not stop the launch. An entry whose name
// matches a calendar that a CalDAV account owns (ErrDeclaredRemote),
// several case variants (ErrAmbiguousName), or a path that another
// calendar uses (ErrOverlappingPath) becomes a warning. A launch that fails
// on a config conflict would also block the commands that resolve it. A
// database error stays an error.
//
// The paths land in a second pass. The pass first clears every path that
// changes, then sets the new ones, so two entries that exchange their
// paths converge in one apply.
//
// Apply runs on every launch, so it must stay idempotent and cheap. It
// reads the calendar list once and writes only the rows that differ.
func (s *Service) Apply(ctx context.Context, declared []Declared) (warnings []string, err error) {
	s.declared = make(map[string]bool, len(declared))
	for _, d := range declared {
		if strings.TrimSpace(d.Name) == "" {
			return nil, fmt.Errorf("calendars: an entry has no name")
		}
	}
	if len(declared) == 0 {
		return nil, nil
	}
	existing, err := s.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}

	// pathChange is a declared path that differs from the stored one.
	type pathChange struct {
		cal  Calendar
		path string
	}
	var changes []pathChange

	for _, d := range declared {
		name := strings.TrimSpace(d.Name)
		cal, ok, err := FindByName(existing, name)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("calendar %q: %v", name, err))
			continue
		}
		if ok && cal.AccountID != 0 && cal.Name != name {
			// A case variant that a CalDAV account owns is not a match.
			// The exact name is free, so the entry creates its own local
			// calendar, as "calendar create" would.
			ok = false
		}
		if ok && cal.AccountID != 0 {
			warnings = append(warnings, fmt.Sprintf("calendar %q: %v", name, ErrDeclaredRemote))
			continue
		}
		// adopted marks a row that existed before this apply. A reset of
		// one of its fields gets a warning; a new row gets none.
		// pathRefused marks a declared path that the create refused, so
		// the second pass does not report the same conflict again.
		adopted, pathRefused := ok, false
		if !ok {
			color := d.Color
			if color == "" {
				color = DefaultColor
			}
			created, err := s.CreateWithPath(ctx, name, color, d.Description, d.Path)
			if errors.Is(err, ErrOverlappingPath) {
				// The calendar still lands, without the path. The path
				// warning below names the conflict, and a later apply sets
				// the path once the conflict is gone.
				warnings = append(warnings, fmt.Sprintf("calendar %q: path %s: %v", name, d.Path, err))
				created, err = s.CreateWithPath(ctx, name, color, d.Description, "")
				pathRefused = true
			}
			if errors.Is(err, ErrDuplicateName) {
				// Two launches at one moment (the TUI and a timer that
				// runs "ical import") both see the calendar missing. The
				// loser adopts the row that the winner created. The row
				// passes the same checks as a row from the first lookup:
				// a remote row or an ambiguous name is a warning, not a
				// failed launch.
				created, err = s.getByName(ctx, name)
				if err == nil && created.AccountID != 0 {
					warnings = append(warnings, fmt.Sprintf("calendar %q: %v", name, ErrDeclaredRemote))
					continue
				}
				if err != nil {
					// An ambiguous name, or a row that went away again,
					// is a transient state of a concurrent launch. The
					// next launch applies the entry.
					warnings = append(warnings, fmt.Sprintf("calendar %q: %v", name, err))
					continue
				}
				adopted = true
			}
			if err != nil {
				return warnings, fmt.Errorf("create calendar %q: %w", name, err)
			}
			cal = created
			existing = append(existing, cal)
		}
		// The guard binds to the stored spelling of the name.
		s.declared[cal.Name] = true
		// A stored value that differs from the declared one comes from a
		// CLI or TUI edit, or from an edit of the file. The entry wins,
		// and a warning names the reset, so an edit never vanishes in
		// silence.
		var reset []string
		if d.Color != "" && !strings.EqualFold(d.Color, cal.Color) {
			reset = append(reset, "color")
		}
		if d.Description != "" && d.Description != cal.Description {
			reset = append(reset, "description")
		}
		if len(reset) > 0 {
			color, description := cal.Color, cal.Description
			if d.Color != "" {
				color = d.Color
			}
			if d.Description != "" {
				description = d.Description
			}
			if _, err := s.Update(ctx, cal.ID, cal.Name, color, description); err != nil {
				return warnings, fmt.Errorf("update calendar %q: %w", name, err)
			}
			if adopted {
				warnings = append(warnings, fmt.Sprintf("calendar %q: the [[calendars]] entry replaced the stored %s", name, strings.Join(reset, ", ")))
			}
		}
		if d.Path != "" && d.Path != cal.LocalPath && !pathRefused {
			changes = append(changes, pathChange{cal: cal, path: d.Path})
		}
	}

	// Second pass: clear every path that changes, then set the new ones.
	// A clear first frees a path that another entry takes over.
	for _, c := range changes {
		if c.cal.LocalPath == "" {
			continue
		}
		if err := s.SetLocalPath(ctx, c.cal.ID, ""); err != nil {
			return warnings, fmt.Errorf("clear path of calendar %q: %w", c.cal.Name, err)
		}
	}
	for _, c := range changes {
		if err := s.SetLocalPath(ctx, c.cal.ID, c.path); err != nil {
			if errors.Is(err, ErrOverlappingPath) {
				// Another calendar, outside the entries, uses the tree.
				// The old path stays cleared, so the two calendars never
				// read one tree; the warning names the conflict.
				warnings = append(warnings, fmt.Sprintf("calendar %q: path %s: %v", c.cal.Name, c.path, err))
				continue
			}
			return warnings, fmt.Errorf("set path of calendar %q: %w", c.cal.Name, err)
		}
		if c.cal.LocalPath != "" {
			warnings = append(warnings, fmt.Sprintf("calendar %q: the [[calendars]] entry replaced the stored path", c.cal.Name))
		}
	}
	return warnings, nil
}
