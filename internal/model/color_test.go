package model

import "testing"

func TestValidateHexColor(t *testing.T) {
	for _, ok := range []string{"#0F766E", "#0f766e", " #AbCdEf "} {
		if err := ValidateHexColor(ok); err != nil {
			t.Errorf("ValidateHexColor(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "0F766E", "teal", "#0F7", "#0F766EFF"} {
		if err := ValidateHexColor(bad); err == nil {
			t.Errorf("ValidateHexColor(%q) accepted a bad color", bad)
		}
	}
	if got := NormalizeHexColor(" #0f766e "); got != "#0F766E" {
		t.Errorf("NormalizeHexColor = %q", got)
	}
}
