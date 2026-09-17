package auth

// ResetKeyringProbe clears the memoized answer of the OS keyring probe. The
// next call probes the backend again.
//
// Use it in a test only. The probe answers once for the whole test binary. A
// test that changes what the probe reads, for example DBUS_SESSION_BUS_ADDRESS,
// must call this function first. An earlier test in the same binary can hold
// the answer of a different environment.
func ResetKeyringProbe() {
	keyringUnavailableReasonFn = newKeyringAvailabilityProbe()
}
