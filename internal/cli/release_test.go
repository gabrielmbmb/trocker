package cli

import (
	"bytes"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestWriteRelease(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		result lifecycle.ReleaseResult
		want   string
	}{
		{
			name:   "nothing matched",
			result: lifecycle.ReleaseResult{},
			want:   "No matching workspace registration; nothing to release.\n",
		},
		{
			name: "inert registration",
			result: lifecycle.ReleaseResult{
				WorkspaceID: "9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18",
				Released:    true,
			},
			want: "Released workspace 9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18\n",
		},
		{
			name: "resources removed",
			result: lifecycle.ReleaseResult{
				WorkspaceID: "9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18",
				Held:        compose.Held{Containers: 2, Networks: 1, Volumes: 1},
				Released:    true,
			},
			want: "Released workspace 9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18\n" +
				"\n  Removed  2 containers, 1 network, 1 volume\n",
		},
		{
			name: "another target keeps the registration",
			result: lifecycle.ReleaseResult{
				WorkspaceID:      "9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18",
				Released:         false,
				RemainingTargets: 1,
			},
			want: "Released workspace 9c4e6f2a-1b3d-4c5e-8f7a-2d0b9e6c4a18\n" +
				"\nRegistration retained for 1 other Docker target(s).\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			if err := writeRelease(&output, test.result); err != nil {
				t.Fatal(err)
			}
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}
