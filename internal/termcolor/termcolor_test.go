package termcolor

import "testing"

func TestColorize(t *testing.T) {
	for _, c := range []struct {
		text, code string
		on         bool
		want       string
	}{
		{"x", BoldCyan, true, "\x1b[1;36mx\x1b[0m"},
		{"x", BoldCyan, false, "x"},
		{"x", "", true, "x"},
		{"", Dim, true, ""},
	} {
		if got := Colorize(c.text, c.code, c.on); got != c.want {
			t.Errorf("Colorize(%q, %q, %v) = %q, want %q", c.text, c.code, c.on, got, c.want)
		}
	}
}
