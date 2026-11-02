package state

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gofrs/flock"
)

// Preferences are verified past mappings, not reservations or current truth.
// This slice supports only local-host TCP/IPv4 loopback publications.
type PortPreference struct {
	WorkspaceID   string `json:"workspace_id"`
	TargetID      string `json:"target_id"`
	Service       string `json:"service"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
}

func (p PortPreference) sameEndpoint(other PortPreference) bool {
	return p.WorkspaceID == other.WorkspaceID && p.TargetID == other.TargetID && p.Service == other.Service && p.ContainerPort == other.ContainerPort
}

func (r Registry) validatePorts() error {
	seen := make(map[PortPreference]bool)
	for _, port := range r.Ports {
		if port.Service == "" || port.ContainerPort == 0 || port.HostPort == 0 {
			return recoveryError()
		}
		registered := false
		for _, workspace := range r.Workspaces {
			if workspace.ID == port.WorkspaceID && slices.Contains(workspace.Targets, port.TargetID) {
				registered = true
				break
			}
		}
		key := port
		key.HostPort = 0
		if !registered || seen[key] {
			return recoveryError()
		}
		seen[key] = true
	}
	return nil
}

func (s Store) RememberPorts(ctx context.Context, ports []PortPreference) error {
	if len(ports) == 0 {
		return nil
	}
	return s.Update(ctx, func(registry *Registry) error {
		for _, port := range ports {
			index := slices.IndexFunc(registry.Ports, func(existing PortPreference) bool { return port.sameEndpoint(existing) })
			if index < 0 {
				registry.Ports = append(registry.Ports, port)
			} else {
				registry.Ports[index] = port
			}
		}
		return nil
	})
}

// TryClaimPort is a nonblocking, operation-owned lease. The OS releases it on
// process death; stale files never constitute claims. All local Docker targets
// share this host namespace, not separate daemon/context namespaces. A lease
// coordinates Trocker processes only and is not a socket reservation.
func (s Store) TryClaimPort(ctx context.Context, port uint16) (*flock.Flock, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if port == 0 {
		return nil, false, fmt.Errorf("cannot claim port zero")
	}
	lock, err := s.newLock(fmt.Sprintf("port-tcp4-loopback-%d", port))
	if err != nil {
		return nil, false, err
	}
	acquired, err := lock.TryLock()
	if err != nil || !acquired {
		return nil, false, errors.Join(err, lock.Close())
	}
	return lock, true, nil
}
