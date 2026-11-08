package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func newInitCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "init",
		Short: "Validate configuration and register this workspace",
		Args:  cobra.NoArgs,
		RunE:  runInit,
	}
	command.Flags().BoolP("dry-run", "n", false, "Report what init would change without writing anything")
	return command
}

func runInit(command *cobra.Command, _ []string) error {
	dryRun, err := command.Flags().GetBool("dry-run")
	if err != nil {
		return err
	}
	// Compose interpolation may need the wrapper's environment, and init
	// validates Compose.
	if err := enterEnvironment(command); err != nil {
		return err
	}
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	stateDirectory, err := state.DefaultDirectory()
	if err != nil {
		return err
	}
	store := state.Store{Directory: stateDirectory}

	if dryRun {
		proposal, err := lifecycle.PrepareInit(command.Context(), directory, store)
		if err != nil {
			return err
		}
		return writeInitProposal(command.OutOrStdout(), proposal)
	}
	result, err := lifecycle.Init(command.Context(), directory, store)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "Workspace registered\n\n  Config     trocker.yaml\n  Workspace  %s\n\nNo containers were started.\n", result.ID)
	return err
}

func writeInitProposal(out io.Writer, proposal lifecycle.InitProposal) error {
	var message strings.Builder
	creating := len(proposal.Manifest) > 0
	fmt.Fprintf(&message, "Initialize workspace\n\n  Checkout   %s\n  Docker     %s\n", proposal.Checkout.Root, proposal.TargetHost)
	if creating {
		message.WriteString("  Config     create trocker.yaml\n")
	} else {
		message.WriteString("  Config     keep trocker.yaml unchanged\n")
	}
	if proposal.ExistingID != "" {
		fmt.Fprintf(&message, "  Workspace  %s (preserved)\n", proposal.ExistingID)
	}
	if proposal.NewTarget {
		message.WriteString("  Target     register Docker association\n")
	}
	if proposal.Moved {
		message.WriteString("  Location   update registered checkout path\n")
	}
	if len(proposal.EnvironmentWrapper) > 0 {
		fmt.Fprintf(&message, "  Wrapper    %s\n", strings.Join(proposal.EnvironmentWrapper, " "))
	}
	if creating {
		message.WriteString("\nProposed trocker.yaml\n\n")
		for line := range strings.SplitSeq(strings.TrimSuffix(string(proposal.Manifest), "\n"), "\n") {
			fmt.Fprintf(&message, "  %s\n", line)
		}
	}
	message.WriteString("\nNothing was written.\n")
	_, err := io.WriteString(out, message.String())
	return err
}
