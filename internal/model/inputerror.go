package model

// InputError marks a service error that reports a request the user must
// change, not a system failure. The CLI gives such an error the
// "invalid_input" code. The marker lives on the error, so a new sentinel
// needs no entry in a list elsewhere. The package sits below every domain
// package, so each domain can mark its own errors.
type InputError interface {
	error
	InvalidInput() bool
}

type inputError struct{ msg string }

func (e *inputError) Error() string      { return e.msg }
func (e *inputError) InvalidInput() bool { return true }

// NewInputError returns a sentinel that satisfies InputError.
func NewInputError(msg string) error { return &inputError{msg: msg} }
