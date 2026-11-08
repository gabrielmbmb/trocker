package lifecycle

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/state"
)

type ReassignResult struct {
	Moved []PortMove
}

type PortMove struct {
	Service       string
	ContainerPort uint16
	From          uint16
	To            uint16
}

// ReassignPorts moves stable assignments to free ports in stable_port_range.
// Without requested publications it moves every assignment inside the host's
// ephemeral range, where programs listening on port zero can take it. Data and
// containers are untouched; the next up publishes the new ports.
func ReassignPorts(ctx context.Context, directory string, store state.Store, requested []plan.PortKey) (result ReassignResult, err error) {
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return result, err
	}
	lock, err := store.LockWorkspace(ctx, registration.Checkout.GitDir)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	registration, err = registeredWorkspace(ctx, directory, store)
	if err != nil {
		return result, err
	}
	manifest, err := config.Load(filepath.Join(registration.Checkout.Root, "trocker.yaml"))
	if err != nil {
		return result, err
	}
	if !manifest.StablePorts {
		return result, &config.Error{Field: "stable_ports", Problem: "reassigning requires stable_ports: true"}
	}
	ephemeral := ephemeralPortRange()
	if err := checkStablePortRange(manifest, ephemeral); err != nil {
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
	assigned := assignmentMap(registry, registration.ID, target.ID)
	keys, err := selectReassignments(assigned, requested, ephemeral)
	if err != nil || len(keys) == 0 {
		return result, err
	}
	// Moving the record under a running container would make ports report a
	// value the container does not publish until it is recreated.
	running, err := target.Containers(ctx, registration, api.PsOptions{})
	if err != nil {
		return result, err
	}
	for _, container := range running {
		if index := slices.IndexFunc(keys, func(key plan.PortKey) bool { return key.Service == container.Service }); index >= 0 {
			return result, &runningPublicationError{key: keys[index]}
		}
	}

	claims := newPortClaims(store, registry)
	defer func() { err = errors.Join(err, claims.Close()) }()
	ports, err := claims.assign(ctx, keys, nil, manifest.StablePortPool())
	if err != nil {
		return result, err
	}
	updated := make([]state.PortAssignment, 0, len(keys))
	for _, key := range keys {
		updated = append(updated, state.PortAssignment{
			WorkspaceID: registration.ID, TargetID: target.ID,
			Service: key.Service, ContainerPort: key.Port, HostPort: ports[key],
		})
		result.Moved = append(result.Moved, PortMove{Service: key.Service, ContainerPort: key.Port, From: assigned[key], To: ports[key]})
	}
	if err := store.AssignPorts(ctx, updated); err != nil {
		return ReassignResult{}, err
	}
	return result, nil
}

// selectReassignments resolves requested publications against the workspace's
// assignments, or selects every assignment inside the ephemeral range when
// none are requested. Each publication appears once, because one pass gives
// each key one new port.
func selectReassignments(assigned map[plan.PortKey]uint16, requested []plan.PortKey, ephemeral config.PortRange) ([]plan.PortKey, error) {
	keys := make([]plan.PortKey, 0, len(requested))
	if len(requested) == 0 {
		for key, port := range assigned {
			if ephemeral.Contains(port) {
				keys = append(keys, key)
			}
		}
	}
	for _, key := range requested {
		if assigned[key] == 0 {
			return nil, &unassignedPortError{key: key}
		}
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b plan.PortKey) int {
		return cmp.Or(strings.Compare(a.Service, b.Service), cmp.Compare(a.Port, b.Port))
	})
	return slices.Compact(keys), nil
}

type unassignedPortError struct{ key plan.PortKey }

func (e *unassignedPortError) Error() string {
	return fmt.Sprintf("%s:%d has no stable host port", e.key.Service, e.key.Port)
}

func (*unassignedPortError) Help() string {
	return "run trocker ports to list assigned publications by service and container port"
}

type runningPublicationError struct{ key plan.PortKey }

func (e *runningPublicationError) Error() string {
	return fmt.Sprintf("%s is running, so %s:%d keeps its stable host port", e.key.Service, e.key.Service, e.key.Port)
}

func (e *runningPublicationError) Help() string {
	return fmt.Sprintf("run trocker stop %s, then retry", e.key.Service)
}
