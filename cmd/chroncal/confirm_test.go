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

// These tests exercise confirmDestructive in isolation. They cover the
// --yes bypass but can't easily exercise the TTY branch — os.Stdin/Stdout
// are not TTYs under `go test`. The end-to-end CLI test at
// delete_confirm_cli_test.go gives the non-interactive refusal path its
// coverage.

func TestConfirmDestructive_YesFlagBypassesPrompt(t *testing.T) {
	cmd := newConfirmCmd()
	if err := cmd.ParseFlags([]string{"--yes"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := confirmDestructive(cmd, "Delete?"); err != nil {
		t.Fatalf("confirmDestructive: %v", err)
	}
	if strings.Contains(errBuf.String(), "[y/N]") {
		t.Errorf("stderr contained prompt despite --yes: %q", errBuf.String())
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
