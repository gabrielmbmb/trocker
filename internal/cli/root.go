package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newRootCommand(version string, output io.Writer) *cobra.Command {
	command := &cobra.Command{
		Use:           "trocker",
		Short:         "Isolated Docker Compose stacks per Git worktree",
		Long:          "Trocker runs isolated Docker Compose stacks per Git worktree.\n\nInit registers a workspace; plan checks isolation; up creates and starts the stack. The ls command lists stacks and their worktrees; status and endpoints inspect the current one. Start, stop, and down manage existing resources. Release stops and forgets a workspace whose worktree is gone. Run executes a local process with native service bindings.",
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	command.SetVersionTemplate("trocker {{.Version}}\n")
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	})
	command.SetHelpCommand(newHelpCommand())
	command.AddCommand(
		newInitCommand(),
		newListCommand(),
		newPlanCommand(),
		newUpCommand(),
		newStartCommand(),
		newStopCommand(),
		newDownCommand(),
		newReleaseCommand(),
		newRunCommand(),
		newStatusCommand(),
		newEndpointsCommand(),
		newDoctorCommand(),
		newVersionCommand(version),
	)
	// Completion generators capture the writer when registered.
	command.SetOut(output)
	command.InitDefaultHelpCmd()
	command.InitDefaultCompletionCmd()
	classifyArgumentErrors(command)
	return command
}

func newVersionCommand(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Trocker version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(command.OutOrStdout(), "trocker %s\n", version)
			return err
		},
	}
}

func newHelpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Show help for a command",
		RunE: func(command *cobra.Command, args []string) error {
			target, remaining, err := command.Root().Find(args)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrUsage, err)
			}
			if len(remaining) != 0 {
				return fmt.Errorf("%w: unknown help topic %q", ErrUsage, args)
			}
			target.InitDefaultHelpFlag()
			target.InitDefaultVersionFlag()
			return target.Help()
		},
	}
}

// Cobra returns untyped validation errors; Trocker reserves exit status 2 for usage.
func classifyArgumentErrors(command *cobra.Command) {
	if validate := command.Args; validate != nil {
		command.Args = func(command *cobra.Command, args []string) error {
			if err := validate(command, args); err != nil {
				return fmt.Errorf("%w: %w", ErrUsage, err)
			}
			return nil
		}
	}
	for _, child := range command.Commands() {
		classifyArgumentErrors(child)
	}
}
