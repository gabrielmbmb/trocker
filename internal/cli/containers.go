package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/native"
)

func newPsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "ps [SERVICE...]",
		Short: "List workspace containers",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, services []string) error {
			all, err := command.Flags().GetBool("all")
			if err != nil {
				return err
			}
			asJSON, err := command.Flags().GetBool("json")
			if err != nil {
				return err
			}
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			result, err := lifecycle.Containers(command.Context(), directory, store, services, all)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(command.OutOrStdout()).Encode(result)
			}
			return writeContainers(command.OutOrStdout(), result, all, services)
		},
	}
	command.Flags().BoolP("all", "a", false, "Show stopped containers and one-off containers")
	command.Flags().Bool("json", false, "Print containers as JSON")
	return command
}

func newLogsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "logs [SERVICE...]",
		Short: "View workspace container logs",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, services []string) error {
			follow, err := command.Flags().GetBool("follow")
			if err != nil {
				return err
			}
			tail, err := command.Flags().GetString("tail")
			if err != nil {
				return err
			}
			since, err := command.Flags().GetString("since")
			if err != nil {
				return err
			}
			until, err := command.Flags().GetString("until")
			if err != nil {
				return err
			}
			timestamps, err := command.Flags().GetBool("timestamps")
			if err != nil {
				return err
			}
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			return lifecycle.Logs(command.Context(), directory, store, compose.LogOptions{
				Services: services, Follow: follow, Tail: tail,
				Since: since, Until: until, Timestamps: timestamps,
			}, rawCommandOutput(command))
		},
	}
	command.Flags().BoolP("follow", "f", false, "Follow log output")
	command.Flags().StringP("tail", "n", "all", "Number of lines to show from the end of each container's logs")
	command.Flags().String("since", "", "Show logs since a timestamp or relative duration")
	command.Flags().String("until", "", "Show logs before a timestamp or relative duration")
	command.Flags().BoolP("timestamps", "t", false, "Show timestamps")
	return command
}

func newExecCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "exec [OPTIONS] SERVICE COMMAND [ARGS...]",
		Short: "Run a command in a running workspace container",
		Long:  "Run a command in a service container, addressed by Compose service name.\nThe command's exit status is returned without an extra Trocker diagnostic.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) < 2 || args[0] == "" || args[1] == "" {
				return fmt.Errorf("expected: trocker exec [OPTIONS] SERVICE COMMAND [ARGS...]")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			detach, err := command.Flags().GetBool("detach")
			if err != nil {
				return err
			}
			environment, err := command.Flags().GetStringArray("env")
			if err != nil {
				return err
			}
			index, err := command.Flags().GetInt("index")
			if err != nil {
				return err
			}
			privileged, err := command.Flags().GetBool("privileged")
			if err != nil {
				return err
			}
			user, err := command.Flags().GetString("user")
			if err != nil {
				return err
			}
			workdir, err := command.Flags().GetString("workdir")
			if err != nil {
				return err
			}
			interactive, err := command.Flags().GetBool("interactive")
			if err != nil {
				return err
			}
			tty, err := command.Flags().GetBool("tty")
			if err != nil {
				return err
			}
			output := rawCommandOutput(command)
			if !command.Flags().Changed("tty") {
				tty = isTerminal(output)
			}
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			code, err := lifecycle.Exec(command.Context(), directory, store, compose.ExecOptions{
				Service: args[0], Command: args[1:], Environment: environment,
				WorkingDir: workdir, User: user, Index: index, Privileged: privileged,
				Detach: detach, Interactive: interactive, Tty: tty,
			}, compose.ExecStreams{In: command.InOrStdin(), Out: output, Err: command.ErrOrStderr()})
			if err != nil {
				return err
			}
			if code != 0 {
				return &native.ExitError{Code: code}
			}
			return nil
		},
	}
	flags := command.Flags()
	flags.BoolP("detach", "d", false, "Run the command in the background")
	flags.StringArrayP("env", "e", nil, "Set an environment variable (KEY=VALUE)")
	flags.Int("index", 0, "Index of the container when the service has multiple replicas")
	flags.Bool("privileged", false, "Give extended privileges to the command")
	flags.StringP("user", "u", "", "Run the command as this user")
	flags.StringP("workdir", "w", "", "Working directory for the command")
	flags.BoolP("interactive", "i", true, "Keep stdin open")
	flags.BoolP("tty", "t", false, "Allocate a pseudo-TTY (default: when stdout is a terminal)")
	flags.SetInterspersed(false)
	return command
}

func writeContainers(out io.Writer, result lifecycle.ContainerResult, all bool, services []string) error {
	if len(result.Containers) != 0 {
		return writeServiceStatuses(out, result.Containers)
	}
	scope := "workspace " + result.WorkspaceID
	if len(services) != 0 {
		scope = "services " + strings.Join(services, ", ")
	}
	if all {
		_, err := fmt.Fprintf(out, "No containers for %s.\n", scope)
		return err
	}
	_, err := fmt.Fprintf(out, "No running containers for %s. Add --all to include stopped containers.\n", scope)
	return err
}
