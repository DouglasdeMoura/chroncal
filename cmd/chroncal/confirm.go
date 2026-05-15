package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// errAborted is the sentinel returned by confirmDestructive when the user
// declined the prompt, the shell is non-interactive without --yes, or any
// other refusal path. main() recognizes it and exits non-zero without
// re-printing — the message has already been written to stderr.
var errAborted = errors.New("aborted")

// confirmDestructive prompts the user to confirm a destructive operation.
//
// Returns nil when the operation should proceed. Returns errAborted (or a
// wrapped IO error) otherwise. All user-facing refusal messages are written
// to stderr; stdout is reserved for command output.
//
// The prompt is skipped (auto-confirmed) in these cases:
//   - --yes / -y was passed
//   - CHRONCAL_ASSUME_YES is set to 1/true/yes
//   - outputFmt is not "text" (scripted, machine-readable)
//
// In a non-interactive shell (stdin or stdout not a TTY) the function
// refuses rather than silently auto-confirming.
//
// question is the full prompt (e.g. `Delete event "Standup"?`). The
// function appends "[y/N] " — default is no.
func confirmDestructive(cmd *cobra.Command, question string) error {
	if yes, _ := cmd.Flags().GetBool("yes"); yes {
		return nil
	}
	if envYes(os.Getenv("CHRONCAL_ASSUME_YES")) {
		return nil
	}
	// Scripted output formats bypass the prompt: a JSON/YAML consumer is
	// scripting explicitly and we shouldn't interleave prompt text with
	// the payload it parses.
	if outputFmt != "text" {
		return nil
	}

	stdin := int(os.Stdin.Fd())
	stdout := int(os.Stdout.Fd())
	if !term.IsTerminal(stdin) || !term.IsTerminal(stdout) {
		// Redirected stdin or stdout. Refusing is safer than silently
		// auto-confirming — the caller likely expected an interactive
		// safety net.
		fmt.Fprintln(cmd.ErrOrStderr(),
			"Refusing destructive operation from a non-interactive shell; pass --yes to confirm.")
		return errAborted
	}

	// Write the prompt to stderr so stdout stays clean for data consumers
	// even in the interactive case.
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "y" || answer == "yes" {
		return nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Aborted.")
	return errAborted
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
