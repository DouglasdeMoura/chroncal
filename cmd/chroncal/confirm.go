package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// confirmDestructive prompts the user to confirm a destructive operation.
//
// The function returns (true, nil) when the operation should proceed. It
// returns (false, nil) when the user explicitly declined at an interactive
// prompt — callers should print a short "Aborted." line and return nil.
// It returns an error only when reading from stdin failed.
//
// The prompt is skipped (auto-confirmed) in any of these cases:
//   - --yes / -y was passed on cmd (cobra --yes flag)
//   - CHRONCAL_ASSUME_YES is set to 1/true/yes
//   - outputFmt is not "text" (scripted, machine-readable)
//   - stdin is not a terminal (pipe or redirect)
//   - also auto-declines when stdout is a pipe and no --yes was given,
//     because printing a prompt nobody can answer would hang
//
// question is the full prompt (e.g. `Delete event "Standup"?`). The
// function appends "[y/N] " — default is no.
func confirmDestructive(cmd *cobra.Command, question string) (bool, error) {
	if yes, _ := cmd.Flags().GetBool("yes"); yes {
		return true, nil
	}
	if envYes(os.Getenv("CHRONCAL_ASSUME_YES")) {
		return true, nil
	}
	// Scripted output formats bypass the prompt: a JSON/YAML consumer is
	// scripting explicitly and we shouldn't interleave prompt text with
	// the payload it parses.
	if outputFmt != "text" {
		return true, nil
	}

	stdin := int(os.Stdin.Fd())
	stdout := int(os.Stdout.Fd())
	if !term.IsTerminal(stdin) || !term.IsTerminal(stdout) {
		// Redirected stdin or stdout. Rather than silently auto-confirm a
		// destructive op from an unsuspecting pipeline, refuse and hint at
		// --yes. Silent auto-confirm here would be dangerous (the caller
		// probably expected an interactive safety net).
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Refusing destructive operation from a non-interactive shell; pass --yes to confirm.\n")
		return false, nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// addConfirmFlag attaches the standard --yes / -y flag to a command. Keep
// the flag docstring uniform so help output reads consistently across every
// destructive verb.
func addConfirmFlag(cmd *cobra.Command) {
	cmd.Flags().BoolP("yes", "y", false, "assume yes on confirmation prompts")
}

func envYes(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y":
		return true
	}
	return false
}
