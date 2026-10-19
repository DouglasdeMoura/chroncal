package calendar

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/fsutil"
	"github.com/douglasdemoura/chroncal/internal/model"
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

// ErrDeclaredCalendar reports a rename, a delete, a link, or a move of a
// calendar that a [[calendars]] entry of config.toml declares. The entry
// binds the calendar to its name and keeps it local. The user removes the
// entry first.
var ErrDeclaredCalendar = model.NewInputError(
	"a [[calendars]] entry in config.toml declares this calendar, so it keeps its name and stays local; remove the entry first")

// ErrDeclaredRemote reports a [[calendars]] entry whose name matches a
// calendar that a CalDAV account owns. The table declares local calendars
// only. An apply would push the declared color to the server and feed the
// remote calendar from a local directory.
var ErrDeclaredRemote = model.NewInputError(
	"a CalDAV account owns a calendar with this name; the [[calendars]] table declares local calendars only")

// ErrRemoteLocalPath reports a local directory on a calendar that a CalDAV
// account owns. An import from the directory would push the files to the
// server, which already owns the collection.
var ErrRemoteLocalPath = model.NewInputError(
	"a CalDAV account owns this calendar, so it cannot have a local directory")

// ErrLocalPathLink reports a link of a calendar that has a local directory
// to a CalDAV account. The user removes the directory first. The text
// names no CLI flag, because the TUI reports the same error.
var ErrLocalPathLink = model.NewInputError(
	"this calendar has a local directory; remove the directory before you link it to a CalDAV account")

// ErrAmbiguousName reports a name that matches several stored calendars
// that differ only in case and none exactly. Apply skips such an entry
// with a warning, and the CLI asks for the numeric ID. The user renames
// one of the calendars.
var ErrAmbiguousName = model.NewInputError(
	"several calendars match this name in another case; rename one of them or use the numeric ID")

// ErrOverlappingPath reports a local directory that another calendar
// already uses, or that lies inside or above the directory of another
// calendar. The import reads a directory and its subdirectories, so two
// calendars on one tree would import the same files and move each todo
// and journal between them on every run.
var ErrOverlappingPath = model.NewInputError(
	"each calendar needs its own directory tree")

// checkPathFree returns ErrOverlappingPath when another calendar than id
// uses path or a directory above or below it. The error names the path and
// the other calendar; the caller adds the subject calendar. The list order
// decides which calendar the error names, so the name is stable across
// launches. The candidate resolves once; each stored path resolves once
// per call.
func checkPathFree(cals []Calendar, id int64, path string) error {
	resolved := make([]string, len(cals))
	for i := range cals {
		resolved[i] = fsutil.ResolveTree(cals[i].LocalPath)
	}
	return checkResolvedPathFree(cals, resolved, id, path)
}

// checkResolvedPathFree is checkPathFree with the stored paths already
// resolved (resolved[i] belongs to cals[i]). The plan of Apply resolves
// each path once and calls it in a loop. The planned path of id itself
// is not a conflict: resolved[i] holds the new path of the change under
// check.
func checkResolvedPathFree(cals []Calendar, resolved []string, id int64, path string) error {
	candidate := fsutil.ResolveTree(path)
	for i, c := range cals {
		if c.ID != id && fsutil.SameResolvedTree(resolved[i], candidate) {
			return fmt.Errorf("path %s overlaps the directory of calendar %q: %w", path, c.Name, ErrOverlappingPath)
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
	s.declaredMu.RLock()
	defer s.declaredMu.RUnlock()
	return s.declared[name]
}

// setDeclared replaces the bound names.
func (s *Service) setDeclared(names map[string]bool) {
	s.declaredMu.Lock()
	defer s.declaredMu.Unlock()
	s.declared = names
}

// refuseDeclared returns ErrDeclaredCalendar when Apply bound an entry to
// a calendar with that name.
func (s *Service) refuseDeclared(name string) error {
	if s.IsDeclared(name) {
		return fmt.Errorf("calendar %q: %w", name, ErrDeclaredCalendar)
	}
	return nil
}

// checkDeclaredRename refuses a rename of a declared calendar, except a
// change of case. FindByName still binds the entry to the row in another
// case, so the rename cannot cause the re-create the guard exists to
// prevent. rebind is true when the guard must follow the new spelling
// once the rename commits (rebindDeclared). The guard moves only after
// the commit, so a rename that fails leaves it on the stored name.
func (s *Service) checkDeclaredRename(from, to string) (rebind bool, err error) {
	if !s.IsDeclared(from) {
		return false, nil
	}
	if strings.EqualFold(from, to) {
		return true, nil
	}
	return false, fmt.Errorf("calendar %q: %w", from, ErrDeclaredCalendar)
}

// CheckRename reports whether a calendar can take a new name: a declared
// calendar changes only its case (ErrDeclaredCalendar), and the name must
// be free in cals (ErrDuplicateName). The CLI calls it before any write,
// so a refused rename costs no write. Update runs the same checks.
func (s *Service) CheckRename(cals []Calendar, from, to string) error {
	if from == to {
		return nil
	}
	if _, err := s.checkDeclaredRename(from, to); err != nil {
		return err
	}
	for _, c := range cals {
		if c.Name == to {
			return ErrDuplicateName
		}
	}
	return nil
}

// rebindDeclared moves the guard from one spelling to another.
func (s *Service) rebindDeclared(from, to string) {
	s.declaredMu.Lock()
	defer s.declaredMu.Unlock()
	delete(s.declared, from)
	s.declared[to] = true
}

// StaysLocal is the one rule for a calendar that cannot become a server
// collection: a declared calendar (ErrDeclaredCalendar) and a calendar
// with a local directory (ErrLocalPathLink). CheckLinkable, the calendar
// move, and the discovery relink share it, so the three cannot drift. A
// nil declared function declares nothing.
func StaysLocal(name, localPath string, declared func(name string) bool) error {
	if declared != nil && declared(name) {
		return fmt.Errorf("calendar %q: %w", name, ErrDeclaredCalendar)
	}
	if localPath != "" {
		return fmt.Errorf("calendar %q: %w", name, ErrLocalPathLink)
	}
	return nil
}

// CheckLinkable reports whether a calendar can take a CalDAV link
// (StaysLocal). ConnectTarget and Connect call it, and the CLI calls it
// before it writes any other field of the calendar, so a refusal costs no
// prompt, no network request, and no partial update.
func (s *Service) CheckLinkable(cal Calendar) error {
	return StaysLocal(cal.Name, cal.LocalPath, s.IsDeclared)
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
// The paths land in a second pass. The pass plans the paths as they stand
// after the apply, warns about a change that would share a tree with
// another calendar, then clears every accepted change and sets the new
// paths, so two entries that exchange their paths converge in one apply.
//
// Apply runs on every launch, so it must stay idempotent and cheap. It
// reads the calendar list once and writes only the rows that differ.
func (s *Service) Apply(ctx context.Context, declared []Declared) (warnings []string, err error) {
	bound := make(map[string]bool, len(declared))
	s.setDeclared(bound)
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

	var changes []pathChange

	for _, d := range declared {
		name := strings.TrimSpace(d.Name)
		cal, ok, err := FindByName(existing, name)
		if ok && err == nil && cal.Name == name && cal.AccountID != 0 {
			warnings = append(warnings, fmt.Sprintf("calendar %q: %v", name, ErrDeclaredRemote))
			continue
		}
		if err != nil || (ok && cal.AccountID != 0) {
			// A case variant that a CalDAV account owns is not a match,
			// and it must not make the name ambiguous: Apply never adopts
			// a remote row. The lookup repeats over the local rows. The
			// exact name is free, so an entry without a local variant
			// creates its own calendar, as "calendar create" would.
			cal, ok, err = FindByName(localOnly(existing), name)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("calendar %q: %v", name, err))
				continue
			}
		}
		// adopted marks a row that existed before this apply. A reset of
		// one of its fields gets a warning; a new row gets none.
		adopted := ok
		if !ok {
			color := d.Color
			if color == "" {
				color = DefaultColor
			}
			// The row lands without its path. The second pass sets the
			// path with every other change, so a new entry that takes a
			// path over from another entry converges in one apply.
			created, err := s.Create(ctx, name, color, d.Description)
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
		s.declaredMu.Lock()
		bound[cal.Name] = true
		s.declaredMu.Unlock()
		// A stored value that differs from the declared one comes from a
		// CLI or TUI edit, or from an edit of the file. The entry wins,
		// and a warning names the reset, so an edit never vanishes in
		// silence.
		// An empty stored value is nothing to replace, so the first apply
		// of a new entry field warns about nothing.
		var reset, replaced []string
		if d.Color != "" && !strings.EqualFold(d.Color, cal.Color) {
			reset = append(reset, "color")
			replaced = append(replaced, "color")
		}
		if d.Description != "" && d.Description != cal.Description {
			reset = append(reset, "description")
			if cal.Description != "" {
				replaced = append(replaced, "description")
			}
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
			if adopted && len(replaced) > 0 {
				warnings = append(warnings, fmt.Sprintf("calendar %q: the [[calendars]] entry replaced the stored %s", name, strings.Join(replaced, ", ")))
			}
		}
		if d.Path != "" && d.Path != cal.LocalPath {
			changes = append(changes, pathChange{ID: cal.ID, Name: cal.Name, From: cal.LocalPath, To: d.Path, Adopted: adopted})
		}
	}
	if len(changes) == 0 {
		return warnings, nil
	}

	// Second pass. Plan the paths as they stand after the apply, then
	// check each change against that state. A change that would share a
	// tree with another calendar warns and keeps its stored path, so a
	// conflict never leaves a calendar without a directory. The accepted
	// changes land in one transaction, clear first and set second, so two
	// entries that exchange their paths converge in one apply, and a
	// concurrent write cannot land between the clear and the set.
	planned := make([]Calendar, len(existing))
	copy(planned, existing)
	for _, c := range changes {
		for i := range planned {
			if planned[i].ID == c.ID {
				planned[i].LocalPath = c.To
			}
		}
	}
	// A refusal reverts one planned path, which can create a conflict for
	// a change that passed before. The check repeats until no change is
	// refused, so every conflict gets its own warning and every
	// independent change lands.
	// Each path resolves once. A revert reuses the resolved stored path.
	resolved := make([]string, len(planned))
	for i := range planned {
		resolved[i] = fsutil.ResolveTree(planned[i].LocalPath)
	}
	resolvedFrom := make(map[int64]string, len(changes))
	for _, c := range changes {
		resolvedFrom[c.ID] = fsutil.ResolveTree(c.From)
	}
	accepted := append([]pathChange(nil), changes...)
	for {
		refused := false
		kept := accepted[:0]
		for _, c := range accepted {
			if err := checkResolvedPathFree(planned, resolved, c.ID, c.To); err != nil {
				warnings = append(warnings, fmt.Sprintf("calendar %q: %v", c.Name, err))
				for i := range planned {
					if planned[i].ID == c.ID {
						planned[i].LocalPath = c.From
						resolved[i] = resolvedFrom[c.ID]
					}
				}
				refused = true
				continue
			}
			kept = append(kept, c)
		}
		accepted = kept
		if !refused {
			break
		}
	}
	if len(accepted) == 0 {
		return warnings, nil
	}
	if err := s.setLocalPaths(ctx, accepted); err != nil {
		if errors.Is(err, ErrOverlappingPath) {
			// A concurrent write took a tree between the plan and the
			// transaction. No path changed. The next launch applies the
			// entries.
			warnings = append(warnings, fmt.Sprintf("[[calendars]] paths not applied: %v", err))
			return warnings, nil
		}
		return warnings, err
	}
	for _, c := range accepted {
		if c.Adopted {
			warnings = append(warnings, fmt.Sprintf("calendar %q: the [[calendars]] entry set the path %s", c.Name, c.To))
		}
	}
	return warnings, nil
}

// localOnly returns the calendars that no CalDAV account owns.
func localOnly(cals []Calendar) []Calendar {
	local := make([]Calendar, 0, len(cals))
	for _, c := range cals {
		if c.AccountID == 0 {
			local = append(local, c)
		}
	}
	return local
}

// pathChange is a declared path that differs from the stored one.
type pathChange struct {
	ID       int64
	Name     string
	From, To string
	// Adopted marks a row that existed before the apply. A user who
	// cleared the directory of a declared calendar then gets a warning
	// when the entry puts it back.
	Adopted bool
}
