package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newConfirmCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "t"}
	addConfirmFlag(cmd)
	return cmd
}

// These tests exercise confirmDestructive in isolation. They cover the two
// bypass paths (--yes flag and non-text output format) but can't easily
// exercise the TTY branch — os.Stdin/Stdout are not TTYs under `go test`.
// The end-to-end CLI test at calendar_cli_test.go gives the non-interactive
// refusal path its coverage.

func TestConfirmDestructive_YesFlagBypassesPrompt(t *testing.T) {
	cmd := newConfirmCmd()
	if err := cmd.ParseFlags([]string{"--yes"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	ok, err := confirmDestructive(cmd, "Delete?")
	if err != nil {
		t.Fatalf("confirmDestructive: %v", err)
	}
	if !ok {
		t.Fatal("--yes should auto-confirm")
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("output contained prompt despite --yes: %q", out.String())
	}
}

func TestConfirmDestructive_NonTextOutputBypassesPrompt(t *testing.T) {
	cmd := newConfirmCmd()
	origFmt := outputFmt
	outputFmt = "json"
	defer func() { outputFmt = origFmt }()

	var out bytes.Buffer
	cmd.SetOut(&out)
	ok, err := confirmDestructive(cmd, "Delete?")
	if err != nil {
		t.Fatalf("confirmDestructive: %v", err)
	}
	if !ok {
		t.Fatal("json output should auto-confirm")
	}
}

func TestEnvYes(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"no":    false,
		"false": false,
		"1":     true,
		"y":     true,
		"yes":   true,
		"TRUE":  true,
		"  1 ":  true,
	}
	for in, want := range cases {
		if got := envYes(in); got != want {
			t.Errorf("envYes(%q) = %v, want %v", in, got, want)
		}
	}
}
