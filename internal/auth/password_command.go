package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/secretcmd"
)

// ErrPasswordSourceConflict reports a credential that holds both a password
// and a password command. Two sources hide which secret the program sends,
// so every write path rejects the pair.
var ErrPasswordSourceConflict = errors.New(
	"a password and a password command are mutually exclusive; set only one")

// HasPasswordCommand reports whether the credential resolves its basic-auth
// password from a command.
func (c Credential) HasPasswordCommand() bool {
	return strings.TrimSpace(c.PasswordCommand) != ""
}

// ValidatePasswordSources reports a conflict between the two basic-auth
// password sources. Call it before a write to the credential store.
func (c Credential) ValidatePasswordSources() error {
	if strings.TrimSpace(c.Password) != "" && c.HasPasswordCommand() {
		return ErrPasswordSourceConflict
	}
	return nil
}

// ResolvePassword returns the basic-auth password of the credential. It runs
// the password command when the credential holds one. The caller must not
// log, print, or store the result.
//
// An access-token credential stays on the OAuth path. ResolvePassword does
// not read AccessToken.
func (c Credential) ResolvePassword(ctx context.Context) (string, error) {
	if err := c.ValidatePasswordSources(); err != nil {
		return "", err
	}
	if !c.HasPasswordCommand() {
		return c.Password, nil
	}
	secret, err := secretcmd.Run(ctx, c.PasswordCommand)
	if err != nil {
		return "", fmt.Errorf("resolve the password command: %w", err)
	}
	return secret, nil
}
