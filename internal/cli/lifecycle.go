package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func newUpCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "up [SERVICE...]",
		Short: "Create and start the isolated workspace stack",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, services []string) error {
			profiles, err := profileSelection(command)
			if err != nil {
				return err
			}
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			detach, err := command.Flags().GetBool("detach")
			if err != nil {
				return err
			}
			build, err := command.Flags().GetBool("build")
			if err != nil {
				return err
			}
			result, err := lifecycle.Up(command.Context(), directory, store, compose.UpOptions{Services: services, Profiles: profiles, Detach: detach, Build: build}, command.ErrOrStderr())
			if err != nil {
				return err
			}
			if detach {
				if _, err := fmt.Fprint(command.OutOrStdout(), "Workspace started.\n\n"); err != nil {
					return err
				}
				return writeEndpoints(command.OutOrStdout(), result)
			}
			_, err = fmt.Fprintln(command.OutOrStdout(), "Workspace session finished.")
			return err
		},
	}
	command.Flags().BoolP("detach", "d", false, "Run containers in the background")
	command.Flags().Bool("build", false, "Build images before starting containers")
	addProfileFlag(command)
	return command
}

func runLifecycle(command *cobra.Command, services []string) error {
	directory, store, err := queryLocation()
	if err != nil {
		return err
	}
	options := compose.LifecycleOptions{Operation: compose.LifecycleOperation(command.Name()), Services: services}
	if options.Operation == compose.Down {
		options.Volumes, err = command.Flags().GetBool("volumes")
		if err != nil {
			return err
		}
	}
	if err := lifecycle.ApplyLifecycle(command.Context(), directory, store, options, command.ErrOrStderr()); err != nil {
		return err
	}
	if options.Operation == compose.Start {
		_, err = fmt.Fprintln(command.OutOrStdout(), "Docker-assigned ports may change. Run trocker endpoints for current addresses.")
	}
	if options.Operation == compose.Down {
		note := "Workspace registration and volumes retained."
		if options.Volumes {
			note = "Workspace registration retained."
		}
		_, err = fmt.Fprintln(command.OutOrStdout(), note)
	}
	return err
}

func newStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start [SERVICE...]",
		Short: "Start existing stopped workspace containers",
		Long:  "Start existing containers without recreating them or applying configuration changes.\nDocker-assigned ports may change; use endpoints for current addresses and up for configuration changes.",
		Args:  cobra.ArbitraryArgs,
		RunE:  runLifecycle,
	}
}

func newStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [SERVICE...]",
		Short: "Stop workspace containers without removing them",
		Args:  cobra.ArbitraryArgs,
		RunE:  runLifecycle,
	}
}

func newDownCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "down",
		Short: "Remove workspace containers and networks; keep volumes",
		Args:  cobra.NoArgs,
		RunE:  runLifecycle,
	}
	command.Flags().BoolP("volumes", "v", false, "Also remove managed named and attached anonymous volumes")
	return command
}
