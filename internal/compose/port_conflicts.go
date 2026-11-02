package compose

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// ConflictingPorts retries only a current Engine networking failure, corroborated
// by an owned container's failed-start state. Docker exposes no distinct bind-
// conflict error code; unknown messages deliberately fail without a retry.
func (t *Target) ConflictingPorts(ctx context.Context, registration workspace.Registration, startErr error) ([]uint16, error) {
	var failure *startupError
	if !errors.As(startErr, &failure) || !errdefs.IsInternal(failure.cause) || !portBindFailure(failure.cause.Error()) {
		return nil, nil
	}
	containers, err := t.Containers(ctx, registration)
	if err != nil {
		return nil, err
	}
	var ports []uint16
	for _, ctr := range containers {
		inspected, err := t.Client.ContainerInspect(ctx, ctr.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, &observationError{cause: err}
		}
		if inspected.Container.Config == nil || inspected.Container.Config.Labels[WorkspaceLabel] != registration.ID {
			return nil, &ownershipError{kind: "container", name: ctr.ID}
		}
		state := inspected.Container.State
		if state == nil || state.Running || !portBindFailure(state.Error) || inspected.Container.HostConfig == nil {
			continue
		}
		for _, bindings := range inspected.Container.HostConfig.PortBindings {
			for _, binding := range bindings {
				if binding.HostIP.String() != "127.0.0.1" {
					continue
				}
				port, err := strconv.ParseUint(binding.HostPort, 10, 16)
				if err == nil && port != 0 {
					ports = append(ports, uint16(port))
				}
			}
		}
	}
	return ports, nil
}

func portBindFailure(message string) bool {
	message = strings.ToLower(message)
	message = strings.TrimPrefix(message, "error response from daemon: ")
	message = strings.TrimPrefix(message, "failed to set up container networking: ")
	return strings.HasPrefix(message, "driver failed programming external connectivity ") && (strings.Contains(message, "port is already allocated") || strings.Contains(message, "address already in use"))
}
