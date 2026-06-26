package main

import (
	"errors"
	"testing"
)

// TestAlarmMissed_RejectsNonPositiveDays guards against issue #140: a
// zero or negative --days value produced an empty/undefined lookback
// window instead of an invalid_input error. Validation runs before
// initApp, so the command must fail without ever touching the database.
func TestAlarmMissed_RejectsNonPositiveDays(t *testing.T) {
	for _, days := range []string{"0", "-1", "-5"} {
		t.Run("days="+days, func(t *testing.T) {
			cmd := alarmMissedCmd()
			if err := cmd.ParseFlags([]string{"--days", days}); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}

			err := cmd.RunE(cmd, nil)
			if err == nil {
				t.Fatalf("--days %s: want error, got nil", days)
			}
			var ce *cliError
			if !errors.As(err, &ce) {
				t.Fatalf("--days %s: want *cliError, got %T: %v", days, err, err)
			}
			if ce.Code != "invalid_input" {
				t.Fatalf("--days %s: code = %q, want invalid_input", days, ce.Code)
			}
		})
	}
}
