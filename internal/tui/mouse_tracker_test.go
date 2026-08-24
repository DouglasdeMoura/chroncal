package tui

import "testing"

// isolateMouseTracker installs a fresh tracker for one test and restores
// the previous tracker when the test ends.
func isolateMouseTracker(t *testing.T) {
	t.Helper()
	saved := defaultMouseTracker
	defaultMouseTracker = &mouseTracker{}
	t.Cleanup(func() { defaultMouseTracker = saved })
}
