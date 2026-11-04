package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/native"
)

func newRunCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "run SERVICE -- COMMAND [ARG...]",
		Short: "Run a local foreground process with native service bindings",
		Long:  "Run a local command in the current directory with native bindings from existing dependencies.\nDependencies must already be running. Explicit native files are rendered before launch.\nSupported on Linux and macOS.",
		Args: func(command *cobra.Command, args []string) error {
			if command.ArgsLenAtDash() != 1 || len(args) <= 1 || args[0] == "" || args[1] == "" {
				return fmt.Errorf("expected: trocker run SERVICE -- COMMAND [ARG...]")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			profiles, err := profileSelection(command)
			if err != nil {
				return err
			}
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			output := rawCommandOutput(command)
			return lifecycle.Run(command.Context(), directory, store, args[0], profiles, args[1:], native.Streams{In: command.InOrStdin(), Out: output, Err: command.ErrOrStderr()})
		},
	}
	addProfileFlag(command)
	return command
}
