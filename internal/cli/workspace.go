package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func newPlanCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Short: "Preview workspace isolation and check ownership",
		Args:  cobra.NoArgs,
		RunE:  runPlan,
	}
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show workspace containers and their state",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			observation, err := lifecycle.Observe(command.Context(), directory, store)
			if err != nil {
				return err
			}
			return writeStatus(command.OutOrStdout(), observation)
		},
	}
}

func newEndpointsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "endpoints",
		Short: "Show observed workspace addresses",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			result, err := lifecycle.Endpoints(command.Context(), directory, store)
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
			return writeEndpoints(command.OutOrStdout(), result)
		},
	}
	command.Flags().Bool("json", false, "Print endpoints as JSON")
	return command
}

func queryLocation() (string, state.Store, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", state.Store{}, err
	}
	store, err := queryStore()
	return directory, store, err
}

func queryStore() (state.Store, error) {
	stateDirectory, err := state.DefaultDirectory()
	return state.Store{Directory: stateDirectory}, err
}

func writeStatus(out io.Writer, observation lifecycle.Observation) error {
	var message strings.Builder
	fmt.Fprintf(&message, "Workspace %s\n\n", observation.WorkspaceID)
	if err := writeServiceStatuses(&message, observation.Services); err != nil {
		return err
	}
	_, err := io.WriteString(out, message.String())
	return err
}

func writeServiceStatuses(out io.Writer, services []lifecycle.ServiceStatus) error {
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SERVICE\tCONTAINER\tSTATE\tHEALTH"); err != nil {
		return err
	}
	for _, service := range services {
		name, health := service.Container, service.Health
		if name == "" {
			name = "-"
		}
		if health == "" {
			health = "-"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", service.Service, name, service.State, health); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeEndpoints(out io.Writer, result lifecycle.EndpointResult) error {
	var message strings.Builder
	if len(result.Endpoints) == 0 {
		message.WriteString("No endpoints configured in trocker.yaml.\n")
	} else {
		table := tabwriter.NewWriter(&message, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(table, "ENDPOINT\tSERVICE\tSTATE\tHOST ADDRESS"); err != nil {
			return err
		}
		for _, endpoint := range result.Endpoints {
			host := "-"
			if endpoint.Addresses.Host != nil {
				address := endpoint.Addresses.Host
				host = address.URL
				if host == "" {
					host = net.JoinHostPort(address.Host, strconv.Itoa(int(address.Port)))
				}
			}
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", endpoint.Name, endpoint.Service, endpoint.State, host); err != nil {
				return err
			}
		}
		if err := table.Flush(); err != nil {
			return err
		}
		message.WriteString("\nPublished addresses do not guarantee application readiness.\n")
	}
	_, err := io.WriteString(out, message.String())
	return err
}

func runPlan(command *cobra.Command, _ []string) error {
	directory, store, err := queryLocation()
	if err != nil {
		return err
	}
	result, err := lifecycle.Plan(command.Context(), directory, store)
	if err != nil {
		return err
	}
	var message strings.Builder
	if command.Name() == "doctor" {
		message.WriteString("Isolation, binding, and ownership checks passed.\n\n")
	}
	mounts := result.WritableMounts()
	buildContexts := result.BuildContexts()
	access := result.DockerAPIAccess()
	hostAccess := result.HostAccess()
	fmt.Fprintf(&message, "Workspace plan\n\n  Workspace  %s\n  Project    %s\n  Networks   %d isolated\n  Volumes    %d retained, isolated\n\n", result.Registration.ID, result.Project.Name, len(result.Project.Networks), len(result.Project.Volumes))
	for _, name := range result.Project.ServiceNames() {
		service := result.Project.Services[name]
		fmt.Fprintf(&message, "  %s: workspace identity, %d loopback publications\n", name, len(service.Ports))
		if service.Build != nil {
			fmt.Fprintf(&message, "    Build tag: %s\n", service.Image)
		}
		for _, mount := range mounts {
			if mount.Service != name {
				continue
			}
			label := "Writable mount"
			if mount.Shared {
				label = "Shared writable mount"
			}
			fmt.Fprintf(&message, "    %s: %s -> %s\n", label, mount.Source, mount.Target)
		}
		for _, context := range buildContexts {
			if context.Service == name {
				fmt.Fprintf(&message, "    Build context: %s -> %s\n", context.Name, context.Source)
			}
		}
		for _, item := range access {
			if item.Service != name {
				continue
			}
			if item.Source == "" {
				message.WriteString("    Docker API: use_api_socket\n")
			} else {
				fmt.Fprintf(&message, "    Docker API: %s\n", item.Source)
			}
		}
		for _, grant := range hostAccess {
			if grant.Service != name {
				continue
			}
			fmt.Fprintf(&message, "    Host access: %s\n", hostAccessSummary(grant))
		}
	}
	if len(mounts) > 0 {
		shared := 0
		for _, mount := range mounts {
			if mount.Shared {
				shared++
			}
		}
		note := ""
		if shared > 0 {
			note = fmt.Sprintf(" (%d shared with the host and other workspaces)", shared)
		}
		fmt.Fprintf(&message, "\n%d writable mounts; containers can modify these host paths%s.\n", len(mounts), note)
	}
	if len(access) > 0 {
		message.WriteString("\nDocker API access is not workspace-scoped; those containers can control the Docker daemon.\n")
	}
	if len(hostAccess) > 0 {
		message.WriteString("\nHost namespaces, capabilities, and devices are not workspace-isolated.\n")
	}
	if result.ContainerFiles > 0 {
		fmt.Fprintf(&message, "\n%d container templates checked; rendered content is validated by up.\n", result.ContainerFiles)
	}
	if result.NativeFiles > 0 {
		fmt.Fprintf(&message, "\n%d native templates checked; rendered content is validated by run.\n", result.NativeFiles)
	}
	if result.PlannedPorts > 0 {
		fmt.Fprintf(&message, "\n%d host ports require coordination at startup; addresses are not allocated by plan.\n", result.PlannedPorts)
	}
	message.WriteString("\nOther host ports will be assigned by Docker. No resources were changed.\n")
	_, err = io.WriteString(command.OutOrStdout(), message.String())
	return err
}

func hostAccessSummary(access lifecycle.ServiceHostAccess) string {
	var parts []string
	if access.PID {
		parts = append(parts, "pid: host")
	}
	if access.IPC {
		parts = append(parts, "ipc: host")
	}
	for _, capability := range access.Capabilities {
		parts = append(parts, "cap_add "+capability)
	}
	for _, device := range access.Devices {
		parts = append(parts, "device "+device.Source+" ("+device.Permissions+")")
	}
	return strings.Join(parts, ", ")
}

func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check isolation, bindings, and resource ownership",
		Args:  cobra.NoArgs,
		RunE:  runPlan,
	}
}
