package lifecycle

import (
	"path/filepath"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"

	"github.com/gabrielmbmb/trocker/internal/state"
)

func TestSummarizeStack(t *testing.T) {
	t.Parallel()

	containers := func(states ...container.ContainerState) []api.ContainerSummary {
		result := make([]api.ContainerSummary, len(states))
		for i, state := range states {
			result[i] = api.ContainerSummary{State: state}
		}
		return result
	}
	tests := []struct {
		name        string
		containers  []api.ContainerSummary
		all         bool
		wantStatus  string
		wantVisible bool
	}{
		{name: "running", containers: containers(container.StateRunning, container.StateRunning), wantStatus: "running(2)", wantVisible: true},
		{name: "default hides stopped", containers: containers(container.StateExited, container.StateRunning), wantStatus: "running(1)", wantVisible: true},
		{name: "default omits stopped stack", containers: containers(container.StateExited), wantVisible: false},
		{name: "all includes stopped", containers: containers(container.StateExited, container.StateRunning, container.StateExited), all: true, wantStatus: "exited(2), running(1)", wantVisible: true},
		{name: "all includes missing containers", all: true, wantStatus: "not created", wantVisible: true},
		{name: "default omits missing containers", wantVisible: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			status, visible := summarizeStack(test.containers, test.all)
			if status != test.wantStatus || visible != test.wantVisible {
				t.Fatalf("summarizeStack() = %q, %v; want %q, %v", status, visible, test.wantStatus, test.wantVisible)
			}
		})
	}
}

func TestCombinedStatusSortsStates(t *testing.T) {
	t.Parallel()

	got := combinedStatus([]string{"running", "exited", "running", "created"})
	if want := "created(1), exited(1), running(2)"; got != want {
		t.Fatalf("combinedStatus() = %q, want %q", got, want)
	}
}

func TestListStacksWithoutRegistrationsDoesNotContactDocker(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("DOCKER_CONTEXT", "")

	stacks, err := ListStacks(t.Context(), state.Store{Directory: filepath.Join(t.TempDir(), "state")}, false)
	if err != nil || len(stacks) != 0 {
		t.Fatalf("ListStacks() = %+v, %v; want no stacks without using Docker", stacks, err)
	}
}
