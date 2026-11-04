package lifecycle

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestSummarizeContainersMapsAndSorts(t *testing.T) {
	t.Parallel()

	registration := workspace.Registration{ID: "1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31"}
	result := summarizeContainers(registration, []api.ContainerSummary{
		{Name: "project-web-1", Service: "web", State: container.StateExited},
		{Name: "project-api-1", Service: "api", State: container.StateRunning, Health: container.Healthy},
	})
	want := []ServiceStatus{
		{Service: "api", Container: "project-api-1", State: "running", Health: "healthy"},
		{Service: "web", Container: "project-web-1", State: "exited"},
	}
	if !reflect.DeepEqual(result.Containers, want) {
		t.Fatalf("containers = %+v, want %+v", result.Containers, want)
	}
	if result.SchemaVersion != 1 || result.WorkspaceID != registration.ID || result.Project != registration.ProjectName() {
		t.Fatalf("unexpected result identity: %+v", result)
	}
}

func TestContainerResultJSONContract(t *testing.T) {
	t.Parallel()

	registration := workspace.Registration{ID: "1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31"}
	identity := `"schema_version":1,"workspace_id":"1b2f9a4c-7d3e-4b21-9c8a-6f0a2d5e8b31","project":"trocker-1b2f9a4c7d3e4b219c8a6f0a2d5e8b31"`
	tests := []struct {
		name       string
		containers []api.ContainerSummary
		want       string
	}{
		{name: "no containers", want: "{" + identity + `,"containers":[]}`},
		{
			name:       "container",
			containers: []api.ContainerSummary{{Name: "project-api-1", Service: "api", State: container.StateRunning, Health: container.Healthy}},
			want:       "{" + identity + `,"containers":[{"service":"api","name":"project-api-1","state":"running","health":"healthy"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(summarizeContainers(registration, test.containers))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.want {
				t.Fatalf("JSON = %s, want %s", data, test.want)
			}
		})
	}
}
