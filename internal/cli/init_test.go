package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestInitProposalShowsChangesBeforeConfirmation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		proposal lifecycle.InitProposal
		want     []string
	}{
		{
			name: "new configuration",
			proposal: lifecycle.InitProposal{
				Manifest: []byte("version: 1\ncompose:\n  - compose.yaml\n"), NewTarget: true,
			},
			want: []string{"Config     create trocker.yaml", "Target     register Docker association", "  version: 1\n  compose:\n    - compose.yaml\n", "Create configuration and register this workspace? [y/N] "},
		},
		{
			name:     "existing moved workspace",
			proposal: lifecycle.InitProposal{ExistingID: "existing-id", Moved: true},
			want:     []string{"Config     keep trocker.yaml unchanged", "Workspace  existing-id (preserved)", "Location   update registered checkout path", "Confirm workspace registration? [y/N] "},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.proposal.Checkout = workspace.Checkout{Root: "/checkout"}
			test.proposal.TargetHost = "unix:///docker.sock"
			var output bytes.Buffer
			if err := writeInitProposal(&output, test.proposal, true); err != nil {
				t.Fatal(err)
			}
			for _, want := range append(test.want, "Checkout   /checkout", "Docker     unix:///docker.sock") {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("proposal missing %q:\n%s", want, &output)
				}
			}
			if !strings.HasSuffix(output.String(), "[y/N] ") {
				t.Fatal("confirmation must follow the complete proposal")
			}
		})
	}
}

func TestInitProposalOmitsDigestAndPrompt(t *testing.T) {
	t.Parallel()
	// Without a terminal there is nobody to answer a prompt, and the digest is
	// reported once on stdout rather than repeated inside the proposal.
	proposal := lifecycle.InitProposal{
		Checkout:   workspace.Checkout{Root: "/checkout"},
		TargetHost: "unix:///docker.sock",
		Digest:     strings.Repeat("a", 64),
	}
	var block bytes.Buffer
	if err := writeInitProposal(&block, proposal, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(block.String(), proposal.Digest) {
		t.Fatalf("proposal must not repeat the digest:\n%s", &block)
	}
	if strings.Contains(block.String(), "[y/N]") {
		t.Fatal("a non-interactive proposal must not prompt")
	}
}

func TestInitRejectsConflictingFlags(t *testing.T) {
	t.Parallel()
	err := Run([]string{"init", "--dry-run", "--confirm", "abc"}, "test", &bytes.Buffer{})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("conflicting flags must be a usage error, got %v", err)
	}
}

func TestInitWithoutTerminalAsksForDigest(t *testing.T) {
	t.Parallel()
	err := Run([]string{"init"}, "test", &bytes.Buffer{})
	var help interface{ Help() string }
	if !errors.As(err, &help) {
		t.Fatalf("expected recovery guidance, got %v", err)
	}
	for _, want := range []string{"--dry-run", "--confirm"} {
		if !strings.Contains(help.Help(), want) {
			t.Fatalf("help must name %s: %q", want, help.Help())
		}
	}
}
