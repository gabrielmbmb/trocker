package compose

import (
	"strings"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
)

func TestRequireServiceSelection(t *testing.T) {
	t.Parallel()

	containers := []api.ContainerSummary{
		{Service: "running", State: container.StateRunning},
		{Service: "oneoff", State: container.StateRunning, Labels: map[string]string{api.OneoffLabel: "True"}},
	}
	tests := []struct {
		name          string
		service       string
		includeOneOff bool
		wantProblem   string
	}{
		{name: "service with containers", service: "running"},
		{name: "missing service", service: "missing", wantProblem: `service "missing" has no container`},
		{name: "one-off hidden unless included", service: "oneoff", wantProblem: `service "oneoff" has no container`},
		{name: "one-off addressable when included", service: "oneoff", includeOneOff: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := requireService(containers, test.service, test.includeOneOff)
			if test.wantProblem == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantProblem) {
				t.Fatalf("error = %v, want problem %q", err, test.wantProblem)
			}
		})
	}
}

func TestRequireRunningServiceSelection(t *testing.T) {
	t.Parallel()

	containers := []api.ContainerSummary{
		{Service: "running", State: container.StateRunning, Labels: map[string]string{api.ContainerNumberLabel: "1"}},
		{Service: "stopped", State: container.StateExited, Labels: map[string]string{api.ContainerNumberLabel: "1"}},
		{Service: "oneoff", State: container.StateRunning, Labels: map[string]string{api.OneoffLabel: "True", api.ContainerNumberLabel: "1"}},
		{Service: "replica", State: container.StateRunning, Labels: map[string]string{api.ContainerNumberLabel: "2"}},
	}
	tests := []struct {
		name        string
		service     string
		index       int
		wantProblem string
	}{
		{name: "running service", service: "running"},
		{name: "stopped service", service: "stopped", wantProblem: `service "stopped" is not running`},
		{name: "one-off container is running", service: "oneoff"},
		{name: "missing replica", service: "replica", index: 1, wantProblem: `service "replica" has no running container #1`},
		{name: "matching replica", service: "replica", index: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := requireRunningService(containers, test.service, test.index)
			if test.wantProblem == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantProblem) {
				t.Fatalf("error = %v, want problem %q", err, test.wantProblem)
			}
		})
	}
}
