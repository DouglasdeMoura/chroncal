package storage

import (
	"strings"
	"testing"
)

func TestFTSQuery_PunctuationOnly(t *testing.T) {
	// Tokens made up entirely of separator characters carry no
	// FTS-significant tokens. They must not be emitted as an empty
	// quoted phrase ("-"*) which FTS5 rejects as a syntax error.
	cases := []string{"-", "!", "?", "--", "-!?", "   -   ", "''"}
	for _, in := range cases {
		if got := FTSQuery(in); got != "" {
			t.Errorf("FTSQuery(%q) = %q, want \"\"", in, got)
		}
	}
}

func TestFTSQuery_MixedDropsPunctuation(t *testing.T) {
	// A real word combined with punctuation-only tokens keeps the word.
	got := FTSQuery("foo - bar")
	if !strings.Contains(got, "\"foo\"*") || !strings.Contains(got, "\"bar\"*") {
		t.Errorf("FTSQuery(%q) = %q, want it to contain both words", "foo - bar", got)
	}
	if strings.Contains(got, "\"-\"*") {
		t.Errorf("FTSQuery(%q) = %q, should not contain empty-phrase token", "foo - bar", got)
	}
}

// TestFTSQuery_MatchesAgainstFTS5 exercises the produced MATCH expression
// against a real FTS5 table to ensure it never triggers a syntax error.
func TestFTSQuery_MatchesAgainstFTS5(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	_ = q

	for _, in := range []string{"-", "!", "-!?", "foo", "foo-bar"} {
		fts := FTSQuery(in)
		if fts == "" {
			// Caller bypasses FTS entirely for empty queries.
			continue
		}
		var rowid int
		err := db.QueryRow("SELECT rowid FROM events_fts WHERE events_fts MATCH ?", fts).Scan(&rowid)
		if err != nil && err.Error() != "sql: no rows in result set" {
			t.Errorf("MATCH for input %q (query %q) errored: %v", in, fts, err)
		}
	}
}
