package errs

import "fmt"

// Error is a user-facing error; the CLI prints its message and exits non-zero.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func New(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}
