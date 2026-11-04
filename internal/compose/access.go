package compose

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
)

// ServiceContainers returns a service's containers from an ownership-validated
// snapshot, including one-off containers only when requested.
func ServiceContainers(containers []api.ContainerSummary, service string, includeOneOff bool) []api.ContainerSummary {
	matches := make([]api.ContainerSummary, 0, len(containers))
	for _, summary := range containers {
		if summary.Service != service {
			continue
		}
		if !includeOneOff && IsOneOff(summary) {
			continue
		}
		matches = append(matches, summary)
	}
	return matches
}

func IsOneOff(summary api.ContainerSummary) bool {
	return strings.EqualFold(summary.Labels[api.OneoffLabel], "true")
}

// requireService confirms that a requested service has a container in the
// workspace snapshot before the SDK selects one by service name.
func requireService(containers []api.ContainerSummary, service string, includeOneOff bool) error {
	if len(ServiceContainers(containers, service, includeOneOff)) > 0 {
		return nil
	}
	return &selectionError{
		problem: fmt.Sprintf("service %q has no container in this workspace", service),
		help:    fmt.Sprintf("run trocker ps to inspect services, or trocker up %s to create it", service),
	}
}

// requireRunningService confirms that exec can address a running container,
// including the requested replica index when one is given.
func requireRunningService(containers []api.ContainerSummary, service string, index int) error {
	var running []api.ContainerSummary
	for _, summary := range ServiceContainers(containers, service, true) {
		if summary.State == container.StateRunning {
			running = append(running, summary)
		}
	}
	if len(running) == 0 {
		return &selectionError{
			problem: fmt.Sprintf("service %q is not running", service),
			help:    fmt.Sprintf("start it with trocker start %s or trocker up %s, then retry", service, service),
		}
	}
	if index > 0 && !hasContainerNumber(running, index) {
		return &selectionError{
			problem: fmt.Sprintf("service %q has no running container #%d", service, index),
			help:    "run trocker ps to inspect the service's containers",
		}
	}
	return nil
}

func hasContainerNumber(containers []api.ContainerSummary, index int) bool {
	for _, summary := range containers {
		number, err := strconv.Atoi(summary.Labels[api.ContainerNumberLabel])
		if err == nil && number == index {
			return true
		}
	}
	return false
}

type selectionError struct {
	problem string
	help    string
}

func (e *selectionError) Error() string { return e.problem }
func (e *selectionError) Help() string  { return e.help }
