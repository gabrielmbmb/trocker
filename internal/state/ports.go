package state

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gofrs/flock"
)

// ErrAssignmentConflict means another workspace claimed the same stable host
// port while this process was choosing candidates. The registry is unchanged;
// callers can re-read it and retry.
var ErrAssignmentConflict = errors.New("stable host port assignment conflicts with an existing assignment")

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

// PortAssignment is a workspace's stable host-port contract for one service
// publication. It is authoritative for stable-port mode and persists across
// down/up, unlike a PortPreference or a live publication.
type PortAssignment struct {
	WorkspaceID   string `json:"workspace_id"`
	TargetID      string `json:"target_id"`
	Service       string `json:"service"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
}

func (p PortAssignment) sameEndpoint(other PortAssignment) bool {
	return p.WorkspaceID == other.WorkspaceID && p.TargetID == other.TargetID && p.Service == other.Service && p.ContainerPort == other.ContainerPort
}

func (r Registry) hasTarget(workspaceID, targetID string) bool {
	for _, workspace := range r.Workspaces {
		if workspace.ID == workspaceID && slices.Contains(workspace.Targets, targetID) {
			return true
		}
	}
	return false
}

func (r Registry) validatePorts() error {
	seen := make(map[PortPreference]bool)
	for _, port := range r.Ports {
		if port.Service == "" || port.ContainerPort == 0 || port.HostPort == 0 {
			return recoveryError()
		}
		key := port
		key.HostPort = 0
		if !r.hasTarget(port.WorkspaceID, port.TargetID) || seen[key] {
			return recoveryError()
		}
		seen[key] = true
	}
	return nil
}

func (r Registry) validateAssignments() error {
	seen := make(map[PortAssignment]bool)
	hosts := make(map[uint16]PortAssignment)
	for _, assignment := range r.Assignments {
		if assignment.Service == "" || assignment.ContainerPort == 0 || assignment.HostPort == 0 {
			return recoveryError()
		}
		key := assignment
		key.HostPort = 0
		if !r.hasTarget(assignment.WorkspaceID, assignment.TargetID) || seen[key] {
			return recoveryError()
		}
		if _, exists := hosts[assignment.HostPort]; exists {
			return recoveryError()
		}
		hosts[assignment.HostPort] = assignment
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

// AssignPorts persists stable host-port assignments. Registry validation
// rejects duplicate endpoint assignments and duplicate host ports.
func (s Store) AssignPorts(ctx context.Context, assignments []PortAssignment) error {
	if len(assignments) == 0 {
		return nil
	}
	return s.Update(ctx, func(registry *Registry) error {
		replaced := make(map[PortAssignment]bool, len(assignments))
		hosts := make(map[uint16]bool, len(assignments))
		for _, assignment := range assignments {
			key := assignment
			key.HostPort = 0
			if replaced[key] || hosts[assignment.HostPort] {
				return ErrAssignmentConflict
			}
			replaced[key], hosts[assignment.HostPort] = true, true
		}
		for _, existing := range registry.Assignments {
			key := existing
			key.HostPort = 0
			if replaced[key] {
				continue
			}
			if hosts[existing.HostPort] {
				return ErrAssignmentConflict
			}
		}
		for _, assignment := range assignments {
			index := slices.IndexFunc(registry.Assignments, func(existing PortAssignment) bool { return assignment.sameEndpoint(existing) })
			if index < 0 {
				registry.Assignments = append(registry.Assignments, assignment)
			} else {
				registry.Assignments[index] = assignment
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
