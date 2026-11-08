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
	missing := make([]plan.PortKey, 0)
	for _, key := range plan.Publications(prepared.manifest, prepared.Project) {
		if mapping[key] == 0 {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return mapping, nil
	}
	seeds, err := stableSeeds(ctx, target, prepared, registry, missing)
	if err != nil {
		return nil, err
	}
	claims := newPortClaims(store, registry)
	defer func() { err = errors.Join(err, claims.Close()) }()
	assigned, err := claims.assign(ctx, missing, seeds, prepared.manifest.StablePortPool())
	if err != nil {
		return nil, err
	}
	created := make([]state.PortAssignment, 0, len(assigned))
	for _, key := range missing {
		mapping[key] = assigned[key]
		created = append(created, state.PortAssignment{
			WorkspaceID: prepared.Registration.ID, TargetID: target.ID,
			Service: key.Service, ContainerPort: key.Port, HostPort: assigned[key],
		})
	}
	if err := store.AssignPorts(ctx, created); err != nil {
		return nil, err
	}
	return mapping, nil
}

// stableSeed lists the ports a new assignment tries before the stable range:
// the port its running container already publishes, a remembered preference,
// and the host port Compose declares.
type stableSeed struct {
	current, preference, compose uint16
}

func stableSeeds(ctx context.Context, target *compose.Target, prepared PlanResult, registry state.Registry, keys []plan.PortKey) (map[plan.PortKey]stableSeed, error) {
	current, err := observedPorts(ctx, target, prepared.Registration, keys)
	if err != nil {
		return nil, err
	}
	seeds := make(map[plan.PortKey]stableSeed, len(keys))
	for _, key := range keys {
		composePort, _ := plan.ComposeHostPort(prepared.source, key)
		seeds[key] = stableSeed{current: current[key], compose: composePort}
	}
	for _, preference := range registry.Ports {
		key := plan.PortKey{Service: preference.Service, Port: preference.ContainerPort}
		if seed, ok := seeds[key]; ok && preference.WorkspaceID == prepared.Registration.ID && preference.TargetID == target.ID {
			seed.preference = preference.HostPort
			seeds[key] = seed
		}
	}
	return seeds, nil
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

// seedPort returns the first seed that is free to assign, or zero. Current and
// remembered ports may have come from dynamic allocation, so one inside the
// ephemeral range is not kept: it would stay exposed to other programs'
// dynamic listeners. A Compose port is the project's explicit choice and is
// kept wherever it is.
func seedPort(seed stableSeed, used map[uint16]bool, ephemeral config.PortRange, available func(uint16) (bool, error)) (uint16, error) {
	candidates := []struct {
		port    uint16
		dynamic bool
	}{{seed.current, true}, {seed.preference, true}, {seed.compose, false}}
	for _, candidate := range candidates {
		if candidate.port == 0 || used[candidate.port] || candidate.dynamic && ephemeral.Contains(candidate.port) {
			continue
		}
		// This workspace's own container holds its current port, so a probe would report it busy.
		if candidate.port == seed.current {
			return candidate.port, nil
		}
		free, err := available(candidate.port)
		if err != nil {
			return 0, err
		}
		if free {
			return candidate.port, nil
		}
	}
	return 0, nil
}

// poolPorts returns count ports from pool that taken does not reject. It
// prefers the first contiguous run, so one workspace's ports stay together,
// and otherwise takes the first free ports. Nil means the pool has too few.
func poolPorts(count int, pool config.PortRange, taken func(uint16) (bool, error)) ([]uint16, error) {
	run := make([]uint16, 0, count)
	free := make([]uint16, 0, count)
	for candidate := int(pool.First); candidate <= int(pool.Last); candidate++ {
		port := uint16(candidate) //nolint:gosec // The loop stays within a uint16 range.
		busy, err := taken(port)
		if err != nil {
			return nil, err
		}
		if busy {
			run = run[:0]
			continue
		}
		run = append(run, port)
		if len(free) < count {
			free = append(free, port)
		}
		if len(run) == count {
			return run, nil
		}
	}
	if len(free) == count {
		return free, nil
	}
	return nil, nil
}

// portClaims holds the leases one assignment pass wins until its assignments
// are persisted, and remembers every port it tried so none is tried twice.
type portClaims struct {
	store  state.Store
	used   map[uint16]bool
	leases []*flock.Flock
}

func newPortClaims(store state.Store, registry state.Registry) *portClaims {
	used := make(map[uint16]bool)
	for _, assignment := range registry.Assignments {
		used[assignment.HostPort] = true
	}
	return &portClaims{store: store, used: used}
}

func (c *portClaims) Close() (err error) {
	for _, lease := range c.leases {
		err = errors.Join(err, lease.Close())
	}
	return err
}

// claim leases port for this pass. A port another Trocker process is claiming
// is reported as lost rather than waited for.
func (c *portClaims) claim(ctx context.Context, port uint16) (bool, error) {
	lease, acquired, err := c.store.TryClaimPort(ctx, port)
	if err != nil {
		return false, err
	}
	c.used[port] = true
	if acquired {
		c.leases = append(c.leases, lease)
	}
	return acquired, nil
}

// assign claims a port for every key: its first free seed, otherwise a port
// from the stable range. It never asks the OS for a port, because the OS
// picks from the ephemeral range where other programs' listeners come and go.
func (c *portClaims) assign(ctx context.Context, keys []plan.PortKey, seeds map[plan.PortKey]stableSeed, pool config.PortRange) (map[plan.PortKey]uint16, error) {
	available := func(port uint16) (bool, error) { return portAvailable(ctx, port) }
	ephemeral := ephemeralPortRange()
	assigned := make(map[plan.PortKey]uint16, len(keys))
	unseeded := make([]plan.PortKey, 0, len(keys))
	for _, key := range keys {
		for {
			port, err := seedPort(seeds[key], c.used, ephemeral, available)
			if err != nil {
				return nil, err
			}
			if port == 0 {
				unseeded = append(unseeded, key)
				break
			}
			won, err := c.claim(ctx, port)
			if err != nil {
				return nil, err
			}
			if won {
				assigned[key] = port
				break
			}
		}
	}
	taken := func(port uint16) (bool, error) {
		if c.used[port] {
			return true, nil
		}
		free, err := available(port)
		return !free, err
	}
	for len(unseeded) > 0 {
		ports, err := poolPorts(len(unseeded), pool, taken)
		if err != nil {
			return nil, err
		}
		if ports == nil {
			return nil, &stablePortRangeError{pool: pool, needed: len(unseeded)}
		}
		claimed := 0
		for _, port := range ports {
			won, err := c.claim(ctx, port)
			if err != nil {
				return nil, err
			}
			if !won {
				break
			}
			assigned[unseeded[claimed]] = port
			claimed++
		}
		unseeded = unseeded[claimed:]
	}
	return assigned, nil
}

type stablePortRangeError struct {
	pool   config.PortRange
	needed int
}

func (e *stablePortRangeError) Error() string {
	return fmt.Sprintf("stable port range %s has fewer than %d free ports", e.pool, e.needed)
}

func (*stablePortRangeError) Help() string {
	return "widen stable_port_range in trocker.yaml, or release workspaces that are no longer used"
}

func portAvailable(ctx context.Context, port uint16) (bool, error) {
	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if errors.Is(err, addressInUse) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := listener.Close(); err != nil {
		return false, err
	}
	wildcard, err := listen.Listen(ctx, "tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port))))
	if errors.Is(err, addressInUse) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, wildcard.Close()
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
