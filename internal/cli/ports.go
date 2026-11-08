package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func newPortsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "ports",
		Short: "Show stable host-port assignments",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			result, err := lifecycle.Ports(command.Context(), directory, store)
			if err != nil {
				return err
			}
			asJSON, err := command.Flags().GetBool("json")
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(command.OutOrStdout()).Encode(result)
			}
			return writePorts(command.OutOrStdout(), result)
		},
	}
	command.Flags().Bool("json", false, "Print stable ports as JSON")
	return command
}

func writePorts(out io.Writer, result lifecycle.PortResult) error {
	if len(result.Ports) == 0 {
		_, err := io.WriteString(out, "No stable host ports are assigned.\n")
		return err
	}
	var message strings.Builder
	table := tabwriter.NewWriter(&message, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SERVICE\tCONTAINER PORT\tHOST PORT"); err != nil {
		return err
	}
	for _, port := range result.Ports {
		if _, err := fmt.Fprintf(table, "%s\t%d\t%d\n", port.Service, port.ContainerPort, port.HostPort); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	message.WriteString("\nStable ports are retained across starts and are never replaced automatically.\n")
	_, err := io.WriteString(out, message.String())
	return err
}
