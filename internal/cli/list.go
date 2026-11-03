package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func newListCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "ls",
		Short: "List running stacks and their worktrees",
		Long:  "List stacks on the selected Docker target and the worktree each runs in.\nUse --all to include stopped stacks and registered worktrees without containers.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			all, err := command.Flags().GetBool("all")
			if err != nil {
				return err
			}
			store, err := queryStore()
			if err != nil {
				return err
			}
			stacks, err := lifecycle.ListStacks(command.Context(), store, all)
			if err != nil {
				return err
			}
			return writeStacks(command.OutOrStdout(), stacks, all)
		},
	}
	command.Flags().BoolP("all", "a", false, "Include stopped stacks and registered worktrees without containers")
	return command
}

func writeStacks(out io.Writer, stacks []lifecycle.Stack, all bool) error {
	if len(stacks) == 0 {
		message := "No running stacks on this Docker target.\n"
		if all {
			message = "No stacks registered with this Docker target.\n"
		}
		_, err := io.WriteString(out, message)
		return err
	}
	var message strings.Builder
	table := tabwriter.NewWriter(&message, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "WORKSPACE\tSTATUS\tWORKTREE"); err != nil {
		return err
	}
	missing, retained := 0, false
	for _, stack := range stacks {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", stack.WorkspaceID, stack.Status, stack.Worktree); err != nil {
			return err
		}
		if stack.Missing {
			missing++
			retained = retained || stack.Volumes > 0
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if missing > 0 {
		message.WriteString("\nRun trocker release <workspace> to stop and forget a workspace whose worktree is gone.\n")
		if retained {
			message.WriteString("Add --volumes to remove the volumes it still owns.\n")
		}
	}
	_, err := io.WriteString(out, message.String())
	return err
}
