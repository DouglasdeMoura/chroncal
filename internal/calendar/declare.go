package calendar

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// ErrDeclaredCalendar reports a rename or a delete of a calendar that a
// [[calendars]] entry of config.toml declares. Apply would create the
// calendar again on the next launch, so the user removes the entry first.
var ErrDeclaredCalendar = errors.New(
	"a [[calendars]] entry in config.toml declares this calendar, so the next launch would create it again; remove the entry first")

// ErrDeclaredRemote reports a [[calendars]] entry whose name matches a
// calendar that a CalDAV account owns. The table declares local calendars
// only. An apply would push the declared color to the server and feed the
// remote calendar from a local directory.
var ErrDeclaredRemote = errors.New(
	"a CalDAV account owns a calendar with this name; the [[calendars]] table declares local calendars only")

// ErrDeclaredLink reports a link of a declared calendar to a CalDAV
// account. The table declares local calendars only, so the link is refused
// up front.
var ErrDeclaredLink = errors.New(
	"a [[calendars]] entry in config.toml declares this calendar, and the table declares local calendars only; remove the entry first")

// ErrRemoteLocalPath reports a local directory on a calendar that a CalDAV
// account owns. An import from the directory would push the files to the
// server, which already owns the collection.
var ErrRemoteLocalPath = errors.New(
	"a CalDAV account owns this calendar, so it cannot have a local directory")

// ErrLocalPathLink reports a link of a calendar that has a local directory
// to a CalDAV account. The user removes the directory first.
var ErrLocalPathLink = errors.New(
	"this calendar has a local directory; remove it with --path \"\" before you link it to a CalDAV account")

// ErrAmbiguousDeclaredName reports a declared name that matches several
// stored calendars that differ only in case. Apply cannot pick one, so it
// skips the entry with a warning. The user renames one of the calendars.
var ErrAmbiguousDeclaredName = errors.New(
	"several calendars match this name in another case; rename one of them")

// isDeclared reports whether Apply bound an entry to a stored calendar with
// that exact name. Apply records the stored spelling, so a stray case
// variant of a declared calendar stays free to delete or rename.
func (s *Service) isDeclared(name string) bool {
	return s.declared[name]
}

// refuseDeclared returns ErrDeclaredCalendar when Apply bound an entry to
// a calendar with that name.
func (s *Service) refuseDeclared(name string) error {
	if s.isDeclared(name) {
		return fmt.Errorf("calendar %q: %w", name, ErrDeclaredCalendar)
	}
	return nil
}

// findByName returns the calendar with that name. An exact match wins. One
// match that differs only in case comes next, so a declared "fastmail"
// adopts a stored "Fastmail" instead of a second calendar. The CLI resolves
// a name the same way, and a second calendar would make every name
// reference ambiguous. Several case variants and no exact match give
// ErrAmbiguousDeclaredName.
func findByName(cals []Calendar, name string) (Calendar, bool, error) {
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
		return Calendar{}, false, ErrAmbiguousDeclaredName
	}
}

// getByName returns the calendar with that name (see findByName).
func (s *Service) getByName(ctx context.Context, name string) (Calendar, error) {
	cals, err := s.List(ctx)
	if err != nil {
		return Calendar{}, err
	}
	c, ok, err := findByName(cals, name)
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
// matches a calendar that a CalDAV account owns (ErrDeclaredRemote), or
// several case variants (ErrAmbiguousDeclaredName), becomes a warning, and
// Apply skips that entry. A launch that fails on a config conflict would
// also block the commands that resolve it. A database error stays an
// error.
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
	for _, d := range declared {
		name := strings.TrimSpace(d.Name)
		cal, ok, err := findByName(existing, name)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("calendar %q: %v", name, err))
			continue
		}
		if ok && cal.AccountID != 0 {
			warnings = append(warnings, fmt.Sprintf("calendar %q: %v", name, ErrDeclaredRemote))
			continue
		}
		if !ok {
			color := d.Color
			if color == "" {
				color = DefaultColor
			}
			created, err := s.CreateWithPath(ctx, name, color, d.Description, d.Path)
			if errors.Is(err, ErrDuplicateName) {
				// Two launches at one moment (the TUI and a timer that
				// runs "ical import") both see the calendar missing. The
				// loser adopts the row that the winner created.
				created, err = s.getByName(ctx, name)
			}
			if err != nil {
				return warnings, fmt.Errorf("create calendar %q: %w", name, err)
			}
			cal = created
			existing = append(existing, cal)
		}
		// The guard binds to the stored spelling of the name.
		s.declared[cal.Name] = true
		if (d.Color != "" && !strings.EqualFold(d.Color, cal.Color)) ||
			(d.Description != "" && d.Description != cal.Description) {
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
		}
		if d.Path != "" && d.Path != cal.LocalPath {
			if err := s.SetLocalPath(ctx, cal.ID, d.Path); err != nil {
				return warnings, fmt.Errorf("set path of calendar %q: %w", name, err)
			}
		}
	}
	return warnings, nil
}
