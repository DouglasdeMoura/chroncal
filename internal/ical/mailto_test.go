package ical

import "testing"

func TestStripMailtoAnyCase(t *testing.T) {
	cases := map[string]string{
		"mailto:ALICE@example.com": "ALICE@example.com",
		"MAILTO:alice@example.com": "alice@example.com",
		"Mailto:bob@example.com":   "bob@example.com",
		"maIlTo:carol@example.com": "carol@example.com",
		"alice@example.com":        "alice@example.com",
		"":                         "",
	}
	for in, want := range cases {
		if got := stripMailto(in); got != want {
			t.Errorf("stripMailto(%q) = %q, want %q", in, got, want)
		}
	}
}
