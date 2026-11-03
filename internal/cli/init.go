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
	command := &cobra.Command{
		Use:   "init",
		Short: "Validate configuration and register this workspace after confirmation",
		Args:  cobra.NoArgs,
		RunE:  runInit,
	}
	command.Flags().BoolP("dry-run", "n", false, "Report the proposal and its digest without writing anything")
	command.Flags().String("confirm", "", "Register this workspace when the proposal still matches this digest")
	return command
}

// terminalError asks for a terminal or a digest-confirmed run.
type terminalError struct{}

func (terminalError) Error() string {
	return "init requires an interactive terminal for confirmation; nothing written"
}

func (terminalError) Help() string {
	return "run init --dry-run, review the proposal, then run init --confirm <digest>"
}

// digestError rejects a confirmation captured against a different proposal.
type digestError struct{}

func (digestError) Error() string {
	return "proposal differs from the confirmed digest; nothing written"
}

func (digestError) Help() string {
	return "run init --dry-run to review the current proposal and confirm its digest"
}

func runInit(command *cobra.Command, _ []string) error {
	dryRun, err := command.Flags().GetBool("dry-run")
	if err != nil {
		return err
	}
	digest, err := command.Flags().GetString("confirm")
	if err != nil {
		return err
	}
	if dryRun && digest != "" {
		return fmt.Errorf("%w: --dry-run cannot be combined with --confirm", ErrUsage)
	}
	if digest == "" && !dryRun {
		if _, terminal := term.GetFdInfo(command.InOrStdin()); !terminal {
			return terminalError{}
		}
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
		if err := writeInitProposal(command.ErrOrStderr(), proposal, false); err != nil {
			return err
		}
		_, err = fmt.Fprintln(command.OutOrStdout(), proposal.Digest)
		return err
	}

	result, err := lifecycle.Init(command.Context(), directory, store, func(proposal lifecycle.InitProposal) (bool, error) {
		if digest != "" {
			if proposal.Digest != digest {
				return false, digestError{}
			}
			return true, nil
		}
		if err := writeInitProposal(command.ErrOrStderr(), proposal, true); err != nil {
			return false, err
		}
		return confirmLine(command.Context(), command.InOrStdin())
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "Workspace registered\n\n  Config     trocker.yaml\n  Workspace  %s\n\nNo containers were started.\n", result.ID)
	return err
}

func writeInitProposal(out io.Writer, proposal lifecycle.InitProposal, prompt bool) error {
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
	}
	if prompt {
		if len(proposal.Manifest) > 0 {
			message.WriteString("\nCreate configuration and register this workspace? [y/N] ")
		} else {
			message.WriteString("\nConfirm workspace registration? [y/N] ")
		}
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
