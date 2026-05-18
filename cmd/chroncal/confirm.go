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

// errAborted is the canonical "user/shell refused a destructive op"
// sentinel. Concrete returns from confirmDestructive carry the same code
// ("aborted") but a more specific message, so main()'s error printer can
// route them uniformly without re-implementing the refusal text here.
var errAborted = &cliError{Code: "aborted", Msg: "aborted"}

// confirmDestructive prompts the user to confirm a destructive operation.
//
// Returns nil when the operation should proceed. Returns a *cliError with
// code "aborted" otherwise. The caller propagates the error; main() prints
// the user-facing message (text or JSON, depending on --output).
//
// The prompt is skipped (auto-confirmed) in these cases:
//   - --yes / -y was passed
//   - CHRONCAL_ASSUME_YES is set to 1/true/yes
//
// In every other case — including --output json — the function
// requires either an interactive TTY confirm or --yes. Auto-confirming
// just because output is machine-readable would make scripted callers
// strictly more dangerous than interactive ones, which is the wrong
// shape for a destructive verb. Refusal is rendered as a structured
// payload by main()'s error printer, so JSON consumers see a parseable
// "code": "aborted" response.
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

	stdin := int(os.Stdin.Fd())
	stdout := int(os.Stdout.Fd())
	if !term.IsTerminal(stdin) || !term.IsTerminal(stdout) {
		return &cliError{
			Code: "aborted",
			Msg:  "Refusing destructive operation from a non-interactive shell; pass --yes to confirm.",
		}
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
	return &cliError{Code: "aborted", Msg: "Aborted."}
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
