package main

import (
	"bytes"
	"testing"

	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// Import warnings surfaced on SyncResult are printed by the silent CLI entry
// points (initial sync after account add, opportunistic push after a write).
// One compact line per warning, prefixed so the user knows where it came
// from — and strictly nothing when there are none, because the opportunistic
// push runs after every single write.
func TestFprintImportWarnings(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	fprintImportWarnings(&buf, []syncPkg.ImportWarning{
		{Path: "/cal/warned.ics", UID: "warned-uid", Message: "VEVENT warned-uid: malformed DTEND, fabricated a 1h span"},
		{Path: "/cal/multi.ics", Message: "VEVENT other-uid: dropped alarm with unusable trigger"},
	})

	got := buf.String()
	want := "import warning: /cal/warned.ics (uid warned-uid): VEVENT warned-uid: malformed DTEND, fabricated a 1h span\n" +
		"import warning: /cal/multi.ics: VEVENT other-uid: dropped alarm with unusable trigger\n"
	if got != want {
		t.Errorf("fprintImportWarnings output =\n%q\nwant\n%q", got, want)
	}

	buf.Reset()
	fprintImportWarnings(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no warnings must print nothing (this runs after every write); got %q", buf.String())
	}
}
