package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/moby/term"
	"github.com/spf13/cobra"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Validate configuration and register this workspace after confirmation",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, terminal := term.GetFdInfo(command.InOrStdin())
			if !terminal {
				return fmt.Errorf("init requires an interactive terminal for confirmation; nothing written")
			}
			directory, err := os.Getwd()
			if err != nil {
				return err
			}
			stateDirectory, err := state.DefaultDirectory()
			if err != nil {
				return err
			}
			result, err := lifecycle.Init(command.Context(), directory, state.Store{Directory: stateDirectory}, func(proposal lifecycle.InitProposal) (bool, error) {
				if err := writeInitProposal(command.ErrOrStderr(), proposal); err != nil {
					return false, err
				}
				return confirmLine(command.Context(), command.InOrStdin())
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Workspace registered\n\n  Config     trocker.yaml\n  Workspace  %s\n\nNo containers were started.\n", result.ID)
			return err
		},
	}
}

func writeInitProposal(out io.Writer, proposal lifecycle.InitProposal) error {
	var message strings.Builder
	fmt.Fprintf(&message, "Initialize workspace\n\n  Checkout   %s\n  Docker     %s\n", proposal.Checkout.Root, proposal.TargetHost)
	if len(proposal.Manifest) > 0 {
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
	if len(proposal.Manifest) > 0 {
		message.WriteString("\nProposed trocker.yaml\n\n")
		for line := range strings.SplitSeq(strings.TrimSuffix(string(proposal.Manifest), "\n"), "\n") {
			fmt.Fprintf(&message, "  %s\n", line)
		}
		message.WriteString("\nCreate configuration and register this workspace? [y/N] ")
	} else {
		message.WriteString("\nConfirm workspace registration? [y/N] ")
	}
	_, err := io.WriteString(out, message.String())
	return err
}

func confirmLine(ctx context.Context, input io.Reader) (bool, error) {
	type result struct {
		line string
		err  error
	}
	ready := make(chan result, 1)
	// Terminal reads are not context-aware. The process exits on cancellation;
	// buffering lets a later completed read finish without blocking the goroutine.
	go func() { line, err := bufio.NewReader(input).ReadString('\n'); ready <- result{line, err} }()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case read := <-ready:
		if read.err != nil {
			return false, read.err
		}
		answer := strings.ToLower(strings.TrimSpace(read.line))
		return answer == "y" || answer == "yes", nil
	}
}
