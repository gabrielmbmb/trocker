package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestInitProposalShowsChanges(t *testing.T) {
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
			want: []string{"Config     create trocker.yaml", "Target     register Docker association", "  version: 1\n  compose:\n    - compose.yaml\n"},
		},
		{
			name:     "existing moved workspace",
			proposal: lifecycle.InitProposal{ExistingID: "existing-id", Moved: true},
			want:     []string{"Config     keep trocker.yaml unchanged", "Workspace  existing-id (preserved)", "Location   update registered checkout path"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.proposal.Checkout = workspace.Checkout{Root: "/checkout"}
			test.proposal.TargetHost = "unix:///docker.sock"
			var output bytes.Buffer
			if err := writeInitProposal(&output, test.proposal); err != nil {
				t.Fatal(err)
			}
			for _, want := range append(test.want, "Checkout   /checkout", "Docker     unix:///docker.sock") {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("proposal missing %q:\n%s", want, &output)
				}
			}
		})
	}
}
