//go:build !windows

package cli

import (
	"os"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

// execInWrapper replaces this process with the wrapper, which receives the
// Trocker executable and args as the command to run inside its environment.
func execInWrapper(wrapper *lifecycle.EnvironmentWrapper, args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	argv := append(slices.Clone(wrapper.Command), self)
	argv = append(argv, args...)
	err = unix.Exec(wrapper.Path, argv, wrappedEnvironment(wrapper))
	return &wrapperExecError{script: wrapper.Command[0], cause: err}
}
