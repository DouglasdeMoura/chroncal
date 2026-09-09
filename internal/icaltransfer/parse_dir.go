package icaltransfer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxDirFiles bounds one directory import. A tool such as vdirsyncer writes
// one .ics file for each event, so a real collection holds many files. The
// bound stops a run that points at a very large tree, for example a home
// directory.
const MaxDirFiles = 10000

// ErrNoICSFiles reports a directory that holds no .ics file.
var ErrNoICSFiles = errors.New("the directory holds no .ics file")

// ErrTooManyICSFiles reports a directory tree above the MaxDirFiles bound.
var ErrTooManyICSFiles = fmt.Errorf("the directory holds more than %d .ics files", MaxDirFiles)

// ParsePath parses one .ics file or one directory of .ics files. It is the
// entry point for every import caller. Use it in place of ParseFile when the
// path comes from a user.
//
// A directory is read one level deep and deeper. A tool such as vdirsyncer
// keeps one directory for each collection, so a parent directory holds many
// collections. The parse merges every collection into one preview.
func ParsePath(path string) (Preview, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Preview{}, fmt.Errorf("open file: %w", err)
	}
	if !info.IsDir() {
		return ParseFile(path)
	}
	return ParseDir(path)
}

// ParseDir parses every .ics file in the tree below dir and merges the
// results into one preview.
//
// The walk skips an entry whose name starts with a dot. That excludes a
// version-control directory and a partial file that a sync tool writes.
// The walk does not follow a symbolic link, so a link loop cannot hang it.
//
// A file that fails to parse becomes a warning. The parse then continues.
// One bad file does not discard a whole collection. ParseDir returns an
// error only when it finds no .ics file, when the tree is above MaxDirFiles,
// or when the walk itself fails.
func ParseDir(dir string) (Preview, error) {
	files, err := collectICSFiles(dir)
	if err != nil {
		return Preview{}, err
	}
	if len(files) == 0 {
		return Preview{}, fmt.Errorf("import %s: %w", dir, ErrNoICSFiles)
	}

	var (
		preview Preview
		seenTZ  = map[string]bool{}
	)
	for _, path := range files {
		label := displayLabel(dir, path)
		one, parseErr := ParseFile(path)
		if parseErr != nil {
			preview.Result.Warnings = append(preview.Result.Warnings,
				fmt.Sprintf("%s: %v", label, parseErr))
			continue
		}
		mergeInto(&preview, one, label, seenTZ)
	}

	preview.Events = len(preview.Result.Events)
	preview.Todos = len(preview.Result.Todos)
	preview.Journals = len(preview.Result.Journals)
	preview.FreeBusy = len(preview.Result.FreeBusy)
	preview.Warnings = append([]string(nil), preview.Result.Warnings...)
	return preview, nil
}

// collectICSFiles returns the .ics files in the tree below dir. WalkDir
// reads each directory in lexical order, so the result is repeatable: two
// files that carry the same UID always land in the same order.
func collectICSFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Keep the root even when the user names a dot-directory. Skip
		// every other dot-entry: a version-control directory, and the
		// partial file that a sync tool writes.
		if path != dir && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".ics") {
			return nil
		}
		if len(files) >= MaxDirFiles {
			return ErrTooManyICSFiles
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrTooManyICSFiles) {
			return nil, fmt.Errorf("import %s: %w", dir, ErrTooManyICSFiles)
		}
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}
	return files, nil
}

// displayLabel returns the path of one file relative to the imported
// directory. A warning then names the file the user can open.
func displayLabel(dir, path string) string {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return filepath.Base(path)
	}
	return relative
}

// mergeInto appends one file's parse to the merged preview. A timezone is
// stored once for each TZID: a per-event file layout repeats the same
// VTIMEZONE in every file, and the import writes one row for each copy.
func mergeInto(preview *Preview, one Preview, label string, seenTZ map[string]bool) {
	result := one.Result
	preview.Result.Events = append(preview.Result.Events, result.Events...)
	preview.Result.Todos = append(preview.Result.Todos, result.Todos...)
	preview.Result.Journals = append(preview.Result.Journals, result.Journals...)
	preview.Result.FreeBusy = append(preview.Result.FreeBusy, result.FreeBusy...)
	preview.Result.SkippedComponents += result.SkippedComponents
	for _, tz := range result.Timezones {
		if seenTZ[tz.TZID] {
			continue
		}
		seenTZ[tz.TZID] = true
		preview.Result.Timezones = append(preview.Result.Timezones, tz)
	}
	for _, warning := range result.Warnings {
		preview.Result.Warnings = append(preview.Result.Warnings, label+": "+warning)
	}
}
