package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

type argumentsKey struct{}

// arguments returns the command line this process was invoked with, which the
// environment wrapper runs again.
func arguments(ctx context.Context) []string {
	if args, ok := ctx.Value(argumentsKey{}).([]string); ok {
		return args
	}
	return nil
}

// enterEnvironment runs this command again inside the manifest's environment
// wrapper. It returns only when no wrapper applies or this process already
// runs inside it; an unapproved wrapper is refused without running it.
func enterEnvironment(command *cobra.Command) error {
	wrapper, err := loadWrapper(command)
	if err != nil || wrapper == nil || wrapper.Active() {
		return err
	}
	if !wrapper.Approved {
		return &unapprovedWrapperError{command: wrapper.Command}
	}
	return execInWrapper(wrapper, arguments(command.Context()))
}

func loadWrapper(command *cobra.Command) (*lifecycle.EnvironmentWrapper, error) {
	directory, store, err := queryLocation()
	if err != nil {
		return nil, err
	}
	return lifecycle.LoadEnvironmentWrapper(command.Context(), directory, store)
}

// wrappedEnvironment marks the child as started by this wrapper. An inherited
// marker is replaced rather than duplicated, because lookups use the first
// occurrence of a name.
func wrappedEnvironment(wrapper *lifecycle.EnvironmentWrapper) []string {
	prefix := lifecycle.EnvironmentMarker + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			environment = append(environment, entry)
		}
	}
	return append(environment, prefix+wrapper.Fingerprint)
}

type unapprovedWrapperError struct{ command []string }

func (e *unapprovedWrapperError) Error() string {
	return fmt.Sprintf("environment wrapper %s is not approved", strings.Join(e.command, " "))
}

func (*unapprovedWrapperError) Help() string { return "run trocker init to review and approve it" }

func (*unapprovedWrapperError) Note() string {
	return "it is new or changed since it was approved, so it has not run"
}

type wrapperExecError struct {
	script string
	cause  error
}

func (e *wrapperExecError) Error() string {
	return "cannot run environment wrapper " + e.script
}

func (*wrapperExecError) Help() string {
	return "check that the script is executable and runs the command it receives"
}

func (e *wrapperExecError) Unwrap() error { return e.cause }
