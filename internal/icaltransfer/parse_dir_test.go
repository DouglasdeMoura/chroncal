package icaltransfer_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
)

// eventICS builds a one-event calendar with the given UID and summary. The
// tests below write one such file for each event, the layout that vdirsyncer
// produces.
func eventICS(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Lisbon\r\n" +
		"BEGIN:STANDARD\r\nDTSTART:19701025T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0000\r\n" +
		"TZNAME:WET\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + summary + "\r\n" +
		"DTSTART:20260421T090000Z\r\nDTEND:20260421T100000Z\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestParsePath_DirectoryTreeMergesEveryCollection covers the vdirsyncer
// layout of issue #776: a parent directory of collection directories, one
// .ics file for each event. Every event lands, and the repeated VTIMEZONE is
// stored once.
func TestParsePath_DirectoryTreeMergesEveryCollection(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "work", "a.ics"), eventICS("dir-a", "Work A"))
	writeFile(t, filepath.Join(root, "work", "b.ics"), eventICS("dir-b", "Work B"))
	writeFile(t, filepath.Join(root, "family", "c.ICS"), eventICS("dir-c", "Family C"))
	// vdirsyncer keeps plain metadata files beside the .ics files.
	writeFile(t, filepath.Join(root, "work", "displayname"), "Work")
	// A dot-directory and a dot-file are both skipped.
	writeFile(t, filepath.Join(root, ".git", "hook.ics"), eventICS("dir-hidden", "Hidden"))
	writeFile(t, filepath.Join(root, "work", ".partial.ics"), eventICS("dir-partial", "Partial"))

	preview, err := icaltransfer.ParsePath(root)
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	if preview.Events != 3 {
		t.Fatalf("events = %d, want 3", preview.Events)
	}
	if len(preview.Result.Timezones) != 1 {
		t.Fatalf("timezones = %d, want 1 (deduplicated by TZID)", len(preview.Result.Timezones))
	}
	got := map[string]bool{}
	for _, e := range preview.Result.Events {
		got[e.UID] = true
	}
	for _, uid := range []string{"dir-a", "dir-b", "dir-c"} {
		if !got[uid] {
			t.Fatalf("UID %q missing from %v", uid, got)
		}
	}
	for _, uid := range []string{"dir-hidden", "dir-partial"} {
		if got[uid] {
			t.Fatalf("UID %q came from a dot-entry and must be skipped", uid)
		}
	}
}

// TestParsePath_SingleFileStillWorks confirms ParsePath keeps the ParseFile
// behavior for a plain file argument.
func TestParsePath_SingleFileStillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.ics")
	writeFile(t, path, eventICS("single-1", "Single"))

	preview, err := icaltransfer.ParsePath(path)
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	if preview.Events != 1 {
		t.Fatalf("events = %d, want 1", preview.Events)
	}
}

// TestParsePath_UnreadableFileBecomesWarning confirms one bad file does not
// discard the rest of the directory. The warning names the file.
func TestParsePath_UnreadableFileBecomesWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "good.ics"), eventICS("warn-good", "Good"))
	writeFile(t, filepath.Join(root, "bad.ics"), "not an icalendar stream at all")

	preview, err := icaltransfer.ParsePath(root)
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	if preview.Events != 1 {
		t.Fatalf("events = %d, want 1", preview.Events)
	}
	if !containsSubstring(preview.Warnings, "bad.ics") {
		t.Fatalf("warnings do not name the bad file: %v", preview.Warnings)
	}
}

// TestParseDir_ComponentWarningNamesItsFile confirms a per-component warning
// keeps the file name, so the user can open the file that produced it.
func TestParseDir_ComponentWarningNamesItsFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "venue.ics"),
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n"+
			"BEGIN:VVENUE\r\nUID:venue-1\r\nEND:VVENUE\r\n"+
			"BEGIN:VEVENT\r\nUID:venue-event\r\nSUMMARY:Kept\r\n"+
			"DTSTART:20260421T090000Z\r\nDTEND:20260421T100000Z\r\nEND:VEVENT\r\n"+
			"END:VCALENDAR\r\n")

	preview, err := icaltransfer.ParseDir(root)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if preview.Events != 1 {
		t.Fatalf("events = %d, want 1", preview.Events)
	}
	if !containsSubstring(preview.Warnings, "venue.ics: ") {
		t.Fatalf("component warning does not name its file: %v", preview.Warnings)
	}
}

// TestParseDir_EmptyDirectoryIsAnError confirms a directory with no .ics file
// reports a named error rather than an empty, silent import.
func TestParseDir_EmptyDirectoryIsAnError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "displayname"), "Work")

	_, err := icaltransfer.ParsePath(root)
	if !errors.Is(err, icaltransfer.ErrNoICSFiles) {
		t.Fatalf("err = %v, want ErrNoICSFiles", err)
	}
	if !strings.Contains(err.Error(), root) {
		t.Fatalf("error does not name the directory: %v", err)
	}
}

// TestParsePath_MissingPathIsWrapped confirms a missing path keeps the
// existing "open file" wrap of the file path.
func TestParsePath_MissingPathIsWrapped(t *testing.T) {
	_, err := icaltransfer.ParsePath(filepath.Join(t.TempDir(), "absent.ics"))
	if err == nil || !strings.Contains(err.Error(), "open file") {
		t.Fatalf("err = %v, want an open file wrap", err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// TestParsePath_RootSymlinkFileIsAnError confirms a symbolic-link file is
// not resolved to its target.
func TestParsePath_RootSymlinkFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "one.ics")
	writeFile(t, target, eventICS("link-root-file", "Link"))
	link := filepath.Join(dir, "alias.ics")
	mustSymlink(t, target, link)

	_, err := icaltransfer.ParsePath(link)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("err = %v, want a symbolic link error", err)
	}
}

// TestParsePath_RootSymlinkDirectoryIsAnError confirms a symbolic-link
// directory is not walked as its target.
func TestParsePath_RootSymlinkDirectoryIsAnError(t *testing.T) {
	dir := t.TempDir()
	collection := filepath.Join(dir, "work")
	writeFile(t, filepath.Join(collection, "a.ics"), eventICS("link-root-dir", "Work"))
	link := filepath.Join(dir, "alias")
	mustSymlink(t, collection, link)

	_, err := icaltransfer.ParsePath(link)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("err = %v, want a symbolic link error", err)
	}
}

// TestParsePath_DirectorySkipsChildSymlink confirms a symbolic-link .ics
// file inside the tree is skipped, so the import cannot leave the tree.
func TestParsePath_DirectorySkipsChildSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(root, "good.ics"), eventICS("link-good", "Good"))
	writeFile(t, filepath.Join(outside, "secret.ics"), eventICS("link-secret", "Secret"))
	mustSymlink(t, filepath.Join(outside, "secret.ics"), filepath.Join(root, "alias.ics"))

	preview, err := icaltransfer.ParsePath(root)
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	if preview.Events != 1 {
		t.Fatalf("events = %d, want 1", preview.Events)
	}
	if preview.Result.Events[0].UID != "link-good" {
		t.Fatalf("UID = %q, want link-good", preview.Result.Events[0].UID)
	}
}
