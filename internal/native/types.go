package native

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type (
	Streams struct {
		In       io.Reader
		Out, Err io.Writer
	}
	Options struct {
		Args        []string
		Directory   string
		Environment map[string]string
		Streams     Streams
	}
)

// ExitError is a child's exit status, never a CLI validation or launch error.
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("native command exited with status %d", e.Code)
}

type launchError struct{ cause error }

func (*launchError) Error() string { return "cannot start native command" }
func (e *launchError) Help() string {
	switch {
	case errors.Is(e.cause, exec.ErrNotFound):
		return "install the executable or check your PATH"
	case errors.Is(e.cause, os.ErrPermission):
		return "check the executable's permissions"
	case errors.Is(e.cause, exec.ErrDot):
		return "use an explicit ./ path for an executable in the current directory"
	default:
		return "check the executable and working directory"
	}
}
func (e *launchError) Unwrap() error { return e.cause }

type executionError struct{ cause error }

func (*executionError) Error() string   { return "native command execution failed" }
func (e *executionError) Unwrap() error { return e.cause }
