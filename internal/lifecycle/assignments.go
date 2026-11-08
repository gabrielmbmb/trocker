package lifecycle

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/gofrs/flock"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func workspaceAssignments(registry state.Registry, workspaceID, targetID string) []state.PortAssignment {
	assignments := make([]state.PortAssignment, 0, len(registry.Assignments))
	for _, assignment := range registry.Assignments {
		if assignment.WorkspaceID == workspaceID && assignment.TargetID == targetID {
			assignments = append(assignments, assignment)
		}
	}
	return assignments
}

func assignmentMap(registry state.Registry, workspaceID, targetID string) map[plan.PortKey]uint16 {
	assignments := make(map[plan.PortKey]uint16)
	for _, assignment := range workspaceAssignments(registry, workspaceID, targetID) {
		assignments[plan.PortKey{Service: assignment.Service, Port: assignment.ContainerPort}] = assignment.HostPort
	}
	return assignments
}

func storedAssignments(registry state.Registry, workspaceID, targetID string) []state.PortAssignment {
	assignments := workspaceAssignments(registry, workspaceID, targetID)
	slices.SortFunc(assignments, func(a, b state.PortAssignment) int {
		if order := strings.Compare(a.Service, b.Service); order != 0 {
			return order
		}
		return cmp.Compare(a.ContainerPort, b.ContainerPort)
	})
	return assignments
}

// ensureStableAssignments creates missing stable host-port assignments for the
// selected publications and returns the complete mapping. Existing assignments
// are authoritative; new ones prefer the current publication, a remembered
// preference, or Compose's concrete host port before choosing a free port.
func ensureStableAssignments(ctx context.Context, store state.Store, prepared PlanResult, target *compose.Target) (map[plan.PortKey]uint16, error) {
	if !prepared.StablePorts {
		return nil, nil
	}
	for attempt := range 3 {
		assignments, err := assignMissingStablePorts(ctx, store, prepared, target)
		if err == nil {
			return assignments, nil
		}
		if !errors.Is(err, state.ErrAssignmentConflict) || attempt == 2 {
			return nil, err
		}
	}
	panic("unreachable")
}

func assignMissingStablePorts(ctx context.Context, store state.Store, prepared PlanResult, target *compose.Target) (mapping map[plan.PortKey]uint16, err error) {
	registry, err := store.Read()
	if err != nil {
		return nil, err
	}
	mapping = assignmentMap(registry, prepared.Registration.ID, target.ID)
	publications := plan.Publications(prepared.manifest, prepared.Project)
	missing := make([]plan.PortKey, 0)
	for _, key := range publications {
		if mapping[key] == 0 {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return mapping, nil
	}

	current, err := observedPorts(ctx, target, prepared.Registration, missing)
	if err != nil {
		return nil, err
	}
	preferences := make(map[plan.PortKey]uint16)
	for _, preference := range registry.Ports {
		if preference.WorkspaceID == prepared.Registration.ID && preference.TargetID == target.ID {
			preferences[plan.PortKey{Service: preference.Service, Port: preference.ContainerPort}] = preference.HostPort
		}
	}
	used := make(map[uint16]bool)
	for _, assignment := range registry.Assignments {
		used[assignment.HostPort] = true
	}
	var leases []*flock.Flock
	defer func() {
		for _, lease := range leases {
			err = errors.Join(err, lease.Close())
		}
	}()
	created := make([]state.PortAssignment, 0, len(missing))
	for _, key := range missing {
		composePort, _ := plan.ComposeHostPort(prepared.source, key)
		for {
			hostPort, err := chooseStablePort(ctx, used, current[key], preferences[key], composePort)
			if err != nil {
				return nil, err
			}
			lease, acquired, err := store.TryClaimPort(ctx, hostPort)
			if err != nil {
				return nil, err
			}
			if !acquired {
				used[hostPort] = true
				continue
			}
			leases = append(leases, lease)
			used[hostPort] = true
			mapping[key] = hostPort
			created = append(created, state.PortAssignment{
				WorkspaceID: prepared.Registration.ID, TargetID: target.ID,
				Service: key.Service, ContainerPort: key.Port, HostPort: hostPort,
			})
			break
		}
	}
	if err := store.AssignPorts(ctx, created); err != nil {
		return nil, err
	}
	return mapping, nil
}

func observedPorts(ctx context.Context, target *compose.Target, registration workspace.Registration, keys []plan.PortKey) (map[plan.PortKey]uint16, error) {
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		return nil, err
	}
	definitions := make(map[string]config.Endpoint, len(keys))
	for _, key := range keys {
		definitions[requiredEndpointName(key)] = config.Endpoint{Service: key.Service, Port: key.Port, Scheme: "tcp"}
	}
	endpoints, err := ResolveEndpoints(definitions, containers)
	if err != nil {
		return nil, err
	}
	current := make(map[plan.PortKey]uint16)
	for _, endpoint := range endpoints {
		if host := endpoint.Addresses.Host; host != nil && host.Host == "127.0.0.1" {
			current[plan.PortKey{Service: endpoint.Service, Port: endpoint.ContainerPort}] = host.Port
		}
	}
	return current, nil
}

func chooseStablePort(ctx context.Context, used map[uint16]bool, current, preference, composePort uint16) (uint16, error) {
	for _, candidate := range []uint16{current, preference, composePort} {
		if candidate == 0 || used[candidate] {
			continue
		}
		if candidate == current {
			return candidate, nil
		}
		available, err := portAvailable(ctx, candidate)
		if err != nil {
			return 0, err
		}
		if available {
			return candidate, nil
		}
	}
	for range 32 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var listen net.ListenConfig
		listener, err := listen.Listen(ctx, "tcp4", "127.0.0.1:0")
		if err != nil {
			return 0, fmt.Errorf("cannot assign a stable host port: %w", err)
		}
		address, ok := listener.Addr().(*net.TCPAddr)
		closeErr := listener.Close()
		if !ok || closeErr != nil {
			return 0, errors.Join(fmt.Errorf("cannot inspect a stable host port candidate"), closeErr)
		}
		candidate := uint16(address.Port) //nolint:gosec // A bound TCP port is in 1..65535.
		if used[candidate] {
			continue
		}
		available, err := portAvailable(ctx, candidate)
		if err != nil {
			return 0, err
		}
		if available {
			return candidate, nil
		}
	}
	return 0, fmt.Errorf("cannot assign a stable host port after 32 attempts")
}

func portAvailable(ctx context.Context, port uint16) (bool, error) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp4", address)
	if errors.Is(err, addressInUse) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	wildcard, err := listen.Listen(ctx, "tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port))))
	if errors.Is(err, addressInUse) {
		return false, listener.Close()
	}
	if err != nil {
		return false, errors.Join(err, listener.Close())
	}
	return true, errors.Join(wildcard.Close(), listener.Close())
}

type PortResult struct {
	SchemaVersion int         `json:"schema_version"`
	WorkspaceID   string      `json:"workspace_id"`
	TargetID      string      `json:"target_id"`
	Ports         []PortEntry `json:"ports"`
}

type PortEntry struct {
	Service       string `json:"service"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
}

// Ports reports persisted stable assignments without loading Compose, claiming
// ports, or observing container state. The values remain available while the
// stack is stopped.
func Ports(ctx context.Context, directory string, store state.Store) (result PortResult, err error) {
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return result, err
	}
	target, err := registeredTarget(ctx, registration)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	registry, err := store.Read()
	if err != nil {
		return result, err
	}
	assignments := storedAssignments(registry, registration.ID, target.ID)
	result = PortResult{
		SchemaVersion: 1,
		WorkspaceID:   registration.ID,
		TargetID:      target.ID,
		Ports:         make([]PortEntry, 0, len(assignments)),
	}
	for _, assignment := range assignments {
		result.Ports = append(result.Ports, PortEntry{Service: assignment.Service, ContainerPort: assignment.ContainerPort, HostPort: assignment.HostPort})
	}
	return result, nil
}
