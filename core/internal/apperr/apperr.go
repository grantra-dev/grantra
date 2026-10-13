// Package apperr holds the errors domain packages return for the API to turn
// into responses: not found, conflict and invalid input.
package apperr

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned for something that does not exist, or that the
// tenant cannot see.
var ErrNotFound = errors.New("not found")

// Conflict is a request that clashes with what is stored, such as a name
// already in use. Its message is shown to the caller.
type Conflict struct{ Msg string }

func (e *Conflict) Error() string { return e.Msg }

// Invalid is input that cannot be accepted. Its message is shown to the caller.
type Invalid struct{ Msg string }

func (e *Invalid) Error() string { return e.Msg }

// Invalidf returns an Invalid error with a formatted message.
func Invalidf(format string, args ...any) error {
	return &Invalid{Msg: fmt.Sprintf(format, args...)}
}

// Conflictf returns a Conflict error with a formatted message.
func Conflictf(format string, args ...any) error {
	return &Conflict{Msg: fmt.Sprintf(format, args...)}
}
