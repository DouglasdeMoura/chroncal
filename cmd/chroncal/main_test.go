package main

import (
	"os"
	"testing"
)

// TestMain turns the OS keyring off for the whole test binary. The auth
// package probes the keyring once per process, so a per-test t.Setenv is too
// late when an earlier in-process test built a credential store. Child
// processes inherit the variable through os.Environ. A test that needs the
// real probe sets the variable to "" with t.Setenv.
func TestMain(m *testing.M) {
	if _, ok := os.LookupEnv("CHRONCAL_SECURITY_DISABLE_KEYRING"); !ok {
		_ = os.Setenv("CHRONCAL_SECURITY_DISABLE_KEYRING", "1")
	}
	os.Exit(m.Run())
}
