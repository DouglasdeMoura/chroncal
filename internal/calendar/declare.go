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

// getByName returns the calendar with that exact name.
func (s *Service) getByName(ctx context.Context, name string) (Calendar, error) {
	cals, err := s.List(ctx)
	if err != nil {
		return Calendar{}, err
	}
	for _, c := range cals {
		if c.Name == name {
			return c, nil
		}
	}
	return Calendar{}, fmt.Errorf("calendar %q not found", name)
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

// refuseDeclared returns ErrDeclaredCalendar when Apply received an entry
// with that name.
func (s *Service) refuseDeclared(name string) error {
	if s.declared[name] {
		return fmt.Errorf("calendar %q: %w", name, ErrDeclaredCalendar)
	}
	return nil
}

// Apply reconciles the declared calendars into the database. For each
// entry it creates a missing calendar, and it updates a stored color,
// description, or path that differs from a non-empty declared value. It
// never deletes a calendar: an entry that goes away from the config leaves
// the calendar and its events in place. After Apply, Update refuses to
// rename a declared calendar, and the delete methods refuse to delete one.
//
// An entry whose name matches a calendar that a CalDAV account owns is an
// error (ErrDeclaredRemote). The table declares local calendars only.
//
// Apply runs on every launch, so it must stay idempotent and cheap. It
// reads the calendar list once and writes only the rows that differ.
func (s *Service) Apply(ctx context.Context, declared []Declared) error {
	s.declared = make(map[string]bool, len(declared))
	for _, d := range declared {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			return fmt.Errorf("calendars: an entry has no name")
		}
		s.declared[name] = true
	}
	if len(declared) == 0 {
		return nil
	}
	existing, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("list calendars: %w", err)
	}
	byName := make(map[string]Calendar, len(existing))
	for _, c := range existing {
		byName[c.Name] = c
	}
	for _, d := range declared {
		name := strings.TrimSpace(d.Name)
		cal, ok := byName[name]
		if ok && cal.AccountID != 0 {
			return fmt.Errorf("calendar %q: %w", name, ErrDeclaredRemote)
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
				return fmt.Errorf("create calendar %q: %w", name, err)
			}
			cal = created
			byName[name] = cal
		} else if (d.Color != "" && d.Color != cal.Color) ||
			(d.Description != "" && d.Description != cal.Description) {
			color, description := cal.Color, cal.Description
			if d.Color != "" {
				color = d.Color
			}
			if d.Description != "" {
				description = d.Description
			}
			updated, err := s.Update(ctx, cal.ID, cal.Name, color, description)
			if err != nil {
				return fmt.Errorf("update calendar %q: %w", name, err)
			}
			cal = updated
			byName[name] = cal
		}
		if d.Path != "" && d.Path != cal.LocalPath {
			if err := s.SetLocalPath(ctx, cal.ID, d.Path); err != nil {
				return fmt.Errorf("set path of calendar %q: %w", name, err)
			}
			cal.LocalPath = d.Path
			byName[name] = cal
		}
	}
	return nil
}
