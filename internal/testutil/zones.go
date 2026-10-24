package testutil

import (
	"testing"
	"time"
)

// LocalZones are the local zones of the date-only tests (issue #875). The
// set holds a zone west of UTC, UTC, and a zone east of UTC. A date-only
// value shows on the day before west of UTC when a reader converts its
// stored instant with .Local(). The fixed offsets keep the tests free of
// the tz database.
var LocalZones = []*time.Location{
	time.FixedZone("America/Sao_Paulo", -3*60*60),
	time.UTC,
	time.FixedZone("Asia/Tokyo", 9*60*60),
}

// ForEachLocalZone runs fn once for each zone of LocalZones, with
// time.Local set to that zone. The change of time.Local is global, so do
// not call t.Parallel in a test that uses this helper.
func ForEachLocalZone(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	for _, loc := range LocalZones {
		t.Run(loc.String(), func(t *testing.T) {
			prev := time.Local
			time.Local = loc
			t.Cleanup(func() { time.Local = prev })
			fn(t)
		})
	}
}
