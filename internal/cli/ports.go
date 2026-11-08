package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/plan"
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
	command.AddCommand(newReassignCommand())
	return command
}

func newReassignCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reassign [SERVICE:PORT...]",
		Short: "Move stable host-port assignments to free ports in the stable range",
		Long:  "Move stable host-port assignments, named by service and container port, to free ports in stable_port_range.\nWithout arguments, move every assignment in the range the OS gives programs listening on port zero.\nThe services must be stopped; the next up publishes the new ports. Volumes and containers are untouched.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			keys := make([]plan.PortKey, 0, len(args))
			for _, arg := range args {
				key, ok := parsePublication(arg)
				if !ok {
					return fmt.Errorf("%w: expected SERVICE:PORT, got %q", ErrUsage, arg)
				}
				keys = append(keys, key)
			}
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			result, err := lifecycle.ReassignPorts(command.Context(), directory, store, keys)
			if err != nil {
				return err
			}
			return writeReassigned(command.OutOrStdout(), result)
		},
	}
}

// parsePublication reads SERVICE:PORT, where PORT is the container port.
// Compose service names cannot contain a colon.
func parsePublication(value string) (plan.PortKey, bool) {
	service, port, found := strings.Cut(value, ":")
	number, err := strconv.ParseUint(port, 10, 16)
	if !found || service == "" || err != nil || number == 0 {
		return plan.PortKey{}, false
	}
	return plan.PortKey{Service: service, Port: uint16(number)}, true
}

func writeReassigned(out io.Writer, result lifecycle.ReassignResult) error {
	if len(result.Moved) == 0 {
		_, err := io.WriteString(out, "No stable host ports needed reassignment.\n")
		return err
	}
	var message strings.Builder
	table := tabwriter.NewWriter(&message, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SERVICE\tCONTAINER PORT\tOLD HOST PORT\tNEW HOST PORT"); err != nil {
		return err
	}
	for _, move := range result.Moved {
		if _, err := fmt.Fprintf(table, "%s\t%d\t%d\t%d\n", move.Service, move.ContainerPort, move.From, move.To); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	message.WriteString("\nRun trocker up to publish the new ports.\n")
	_, err := io.WriteString(out, message.String())
	return err
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
	message.WriteString("\nStable ports are retained across starts and are never replaced automatically; trocker ports reassign moves them.\n")
	_, err := io.WriteString(out, message.String())
	return err
}
