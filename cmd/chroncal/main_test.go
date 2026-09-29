package main

import (
	"os"
	"testing"
)

// TestMain turns the OS keyring off for the whole test binary. The auth
// package probes the keyring once per process, so a per-test t.Setenv is too
// late when an earlier in-process test built a credential store. Child
// processes inherit the variable through os.Environ.
//
// The top-level binary sets the value always, so a "0" or "false" in the
// parent environment cannot turn the real keyring back on. A helper child
// process (GO_WANT_HELPER_PROCESS=1) runs this TestMain again. It keeps the
// value from its parent test, so a test that needs the real probe can set the
// variable to "" with t.Setenv.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		_ = os.Setenv("CHRONCAL_SECURITY_DISABLE_KEYRING", "1")
	}
	os.Exit(m.Run())
}
