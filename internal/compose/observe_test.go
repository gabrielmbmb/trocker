package compose_test

import (
	"testing"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestObservationRequiresWorkspaceOwnership(t *testing.T) {
	t.Parallel()
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	for _, label := range []string{"", "another-workspace", registration.ID} {
		ctr := api.ContainerSummary{Project: registration.ProjectName(), Labels: map[string]string{compose.WorkspaceLabel: label}}
		err := compose.ValidateOwnership([]api.ContainerSummary{ctr}, registration)
		if (err == nil) != (label == registration.ID) {
			t.Fatalf("ownership %q: %v", label, err)
		}
	}
	if err := compose.ValidateOwnership([]api.ContainerSummary{{Project: "another-project", Labels: map[string]string{compose.WorkspaceLabel: registration.ID}}}, registration); err == nil {
		t.Fatal("different project accepted")
	}
}
