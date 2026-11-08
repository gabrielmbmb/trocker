package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func newReleaseCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "release [WORKSPACE]",
		Short: "Stop and forget a workspace, including one whose worktree is gone",
		Long: "Stop whatever the workspace still owns on this Docker target and delete its registration.\n" +
			"Without WORKSPACE, release the workspace of the current directory. WORKSPACE may be any unambiguous id prefix printed by ls.\n" +
			"Retained volumes keep the registration unless --volumes is set.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			directory, store, err := queryLocation()
			if err != nil {
				return err
			}
			volumes, err := command.Flags().GetBool("volumes")
			if err != nil {
				return err
			}
			reference := ""
			if len(args) == 1 {
				reference = args[0]
			}
			result, err := lifecycle.Release(command.Context(), directory, store, reference, volumes, command.ErrOrStderr())
			if err != nil {
				return err
			}
			return writeRelease(command.OutOrStdout(), result)
		},
	}
	command.Flags().BoolP("volumes", "v", false, "Also remove volumes the workspace owns")
	return command
}

func releasedSummary(result lifecycle.ReleaseResult) string {
	parts := make([]string, 0, 2)
	if !result.Held.Empty() {
		parts = append(parts, result.Held.Summary())
	}
	switch result.Images {
	case 0:
	case 1:
		parts = append(parts, "1 image")
	default:
		parts = append(parts, fmt.Sprintf("%d images", result.Images))
	}
	return strings.Join(parts, ", ")
}

func writeRelease(out io.Writer, result lifecycle.ReleaseResult) error {
	var message strings.Builder
	if result.WorkspaceID == "" {
		message.WriteString("No matching workspace registration; nothing to release.\n")
		_, err := io.WriteString(out, message.String())
		return err
	}
	fmt.Fprintf(&message, "Released workspace %s\n", result.WorkspaceID)
	if removed := releasedSummary(result); removed != "" {
		fmt.Fprintf(&message, "\n  Removed  %s\n", removed)
	}
	if !result.Released {
		fmt.Fprintf(&message, "\nRegistration retained for %d other Docker target(s).\n", result.RemainingTargets)
	}
	_, err := io.WriteString(out, message.String())
	return err
}
