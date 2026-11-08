package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/gofrs/flock"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/state"
)

type portAllocation struct {
	ports  map[plan.PortKey]uint16
	leases []*flock.Flock
}

func (a *portAllocation) Close() (err error) {
	for _, lease := range a.leases {
		err = errors.Join(err, lease.Close())
	}
	return err
}

func allocatePorts(ctx context.Context, store state.Store, prepared PlanResult, target *compose.Target, requirements []plan.Requirement, excluded map[uint16]bool) (allocation *portAllocation, err error) {
	allocation = &portAllocation{ports: make(map[plan.PortKey]uint16)}
	defer func() {
		if err != nil {
			err = errors.Join(err, allocation.Close())
		}
	}()
	if len(requirements) == 0 {
		return allocation, nil
	}
	keys := make([]plan.PortKey, 0, len(requirements))
	for _, requirement := range requirements {
		keys = append(keys, requirement.Key)
	}
	current, err := observedPorts(ctx, target, prepared.Registration, keys)
	if err != nil {
		return allocation, err
	}
	registry, err := store.Read()
	if err != nil {
		return allocation, err
	}
	reserved := reservedPorts(registry, requirements, excluded)
	for _, requirement := range requirements {
		key := requirement.Key
		if requirement.HostPort != 0 {
			lease, err := claimStablePort(ctx, store, requirement, current[key] == requirement.HostPort)
			if err != nil {
				return allocation, err
			}
			allocation.ports[key] = requirement.HostPort
			allocation.leases = append(allocation.leases, lease)
			continue
		}
		preferred := current[key]
		if preferred == 0 {
			for _, preference := range registry.Ports {
				if preference.WorkspaceID == prepared.Registration.ID && preference.TargetID == target.ID && preference.Service == key.Service && preference.ContainerPort == key.Port {
					preferred = preference.HostPort
					break
				}
			}
		}
		port, lease, err := claimAvailablePort(ctx, store, preferred, current[key] != 0, reserved)
		if err != nil {
			return allocation, err
		}
		allocation.ports[key] = port
		allocation.leases = append(allocation.leases, lease)
	}
	return allocation, nil
}

// reservedPorts prevents dynamic allocation from taking a port another
// workspace has reserved as stable, even while that workspace is stopped.
func reservedPorts(registry state.Registry, requirements []plan.Requirement, excluded map[uint16]bool) map[uint16]bool {
	reserved := make(map[uint16]bool, len(excluded)+len(registry.Assignments)+len(requirements))
	for port := range excluded {
		reserved[port] = true
	}
	for _, assignment := range registry.Assignments {
		reserved[assignment.HostPort] = true
	}
	for _, requirement := range requirements {
		if requirement.HostPort != 0 {
			reserved[requirement.HostPort] = true
		}
	}
	return reserved
}

// claimStablePort claims exactly the assigned port. Unlike a remembered
// preference, an unavailable stable port is an error, never a fallback.
func claimStablePort(ctx context.Context, store state.Store, requirement plan.Requirement, running bool) (*flock.Flock, error) {
	port := requirement.HostPort
	lease, acquired, err := store.TryClaimPort(ctx, port)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, &stablePortError{requirement: requirement}
	}
	fail := func(cause error) (*flock.Flock, error) {
		return nil, errors.Join(&stablePortError{requirement: requirement, cause: cause}, lease.Close())
	}
	if running {
		return lease, nil
	}
	available, err := portAvailable(ctx, port)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("cannot probe a stable host port: %w", err), lease.Close())
	}
	if !available {
		return fail(nil)
	}
	return lease, nil
}

func claimAvailablePort(ctx context.Context, store state.Store, preferred uint16, running bool, excluded map[uint16]bool) (uint16, *flock.Flock, error) {
	for attempt := range 32 {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		port := uint16(0)
		if attempt == 0 && !excluded[preferred] {
			port = preferred
		}
		var listener net.Listener
		// A verified live publication is already bound by this workspace. All other
		// candidates are probed, including remembered ports. Docker still decides
		// actual availability after we release this temporary socket.
		if port == 0 || !running {
			var err error
			var listen net.ListenConfig
			listener, err = listen.Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
			if errors.Is(err, addressInUse) {
				continue
			}
			if err != nil {
				return 0, nil, fmt.Errorf("cannot probe a local publication port: %w", err)
			}
			address, ok := listener.Addr().(*net.TCPAddr)
			if !ok {
				return 0, nil, errors.Join(fmt.Errorf("unexpected local listener address"), listener.Close())
			}
			port = uint16(address.Port) //nolint:gosec // A bound TCP port is in 1..65535.
		}
		var lease *flock.Flock
		var acquired bool
		var err error
		if !excluded[port] {
			lease, acquired, err = store.TryClaimPort(ctx, port)
		}
		if listener != nil {
			err = errors.Join(err, listener.Close())
		}
		if err != nil {
			if lease != nil {
				err = errors.Join(err, lease.Close())
			}
			return 0, nil, err
		}
		if acquired {
			if listener != nil {
				// BSD permits a reuse-address loopback probe alongside a wildcard
				// listener. Probe the wildcard separately too, without serving data.
				var probe net.ListenConfig
				wildcard, probeErr := probe.Listen(ctx, "tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port))))
				if probeErr != nil {
					closeErr := lease.Close()
					if errors.Is(probeErr, addressInUse) && closeErr == nil {
						continue
					}
					return 0, nil, errors.Join(probeErr, closeErr)
				}
				if err := wildcard.Close(); err != nil {
					return 0, nil, errors.Join(err, lease.Close())
				}
			}
			return port, lease, nil
		}
	}
	return 0, nil, &allocationError{}
}

func portRequirements(prepared PlanResult, stable map[plan.PortKey]uint16) ([]plan.Requirement, error) {
	if !prepared.StablePorts {
		required, err := plan.RequiredPorts(prepared.manifest, prepared.Project)
		if err != nil {
			return nil, err
		}
		requirements := make([]plan.Requirement, 0, len(required))
		for _, key := range required {
			requirements = append(requirements, plan.Requirement{Key: key})
		}
		return requirements, nil
	}
	publications := plan.Publications(prepared.manifest, prepared.Project)
	requirements := make([]plan.Requirement, 0, len(publications))
	for _, key := range publications {
		hostPort := stable[key]
		if hostPort == 0 {
			return nil, fmt.Errorf("stable port assignment is missing for %s:%d; retry trocker init or trocker up", key.Service, key.Port)
		}
		requirements = append(requirements, plan.Requirement{Key: key, HostPort: hostPort})
	}
	return requirements, nil
}

func startPlanned(ctx context.Context, store state.Store, prepared PlanResult, target *compose.Target, options compose.UpOptions, stable map[plan.PortKey]uint16, output io.Writer) (*portAllocation, error) {
	requirements, err := portRequirements(prepared, stable)
	if err != nil {
		return nil, err
	}
	templates, err := loadContainerTemplates(ctx, prepared)
	if err != nil {
		return nil, err
	}
	fixed := make(map[uint16]plan.Requirement)
	for _, requirement := range requirements {
		if requirement.HostPort != 0 {
			fixed[requirement.HostPort] = requirement
		}
	}
	excluded := make(map[uint16]bool)
	for attempt := range 3 {
		allocation, err := allocatePorts(ctx, store, prepared, target, requirements, excluded)
		if err != nil {
			return nil, err
		}
		project, err := plan.Build(prepared.manifest, prepared.source, prepared.Registration, plan.Options{Services: options.Services, Ports: allocation.ports})
		if err == nil {
			err = prepareContainerFiles(ctx, store, prepared, target.ID, project, plan.Options{Ports: allocation.ports}, templates)
		}
		if err == nil {
			err = target.Up(ctx, project, options, output)
		}
		if err == nil {
			return allocation, nil
		}
		failed, inspectErr := target.ConflictingPorts(ctx, prepared.Registration, err)
		closeErr := allocation.Close()
		if ctx.Err() != nil {
			return nil, errors.Join(ctx.Err(), closeErr)
		}
		for _, port := range failed {
			if requirement, ok := fixed[port]; ok {
				return nil, errors.Join(&stablePortError{requirement: requirement, cause: err}, closeErr)
			}
		}
		matched := false
		for _, port := range failed {
			for _, planned := range allocation.ports {
				if port == planned {
					excluded[port] = true
					matched = true
				}
			}
		}
		if !matched || inspectErr != nil || closeErr != nil {
			return nil, errors.Join(err, inspectErr, closeErr)
		}
		if attempt == 2 {
			return nil, &allocationError{}
		}
		const message = "Port conflict; retrying startup with updated bindings.\n"
		if n, err := io.WriteString(output, message); err != nil {
			return nil, err
		} else if n != len(message) {
			return nil, io.ErrShortWrite
		}
		options.Build = false // The successful build preceded the bind failure.
	}
	panic("unreachable")
}

func rememberAllocations(ctx context.Context, store state.Store, prepared PlanResult, targetID string, allocation *portAllocation, endpoints []Endpoint, detached bool) error {
	preferences := make([]state.PortPreference, 0, len(allocation.ports))
	for key, port := range allocation.ports {
		verified := false
		for _, endpoint := range endpoints {
			if endpoint.Service == key.Service && endpoint.ContainerPort == key.Port {
				if host := endpoint.Addresses.Host; host != nil {
					if host.Host != "127.0.0.1" || host.Port != port {
						return fmt.Errorf("published address differs from the prepared binding; retry trocker up")
					}
					verified = true
				}
			}
		}
		if !verified {
			// Foreground services may have exited normally; never invent a published
			// address or persist an unverified preference for an exited service.
			if detached {
				return fmt.Errorf("a required endpoint is not published; check service state and retry trocker up")
			}
			continue
		}
		preferences = append(preferences, state.PortPreference{WorkspaceID: prepared.Registration.ID, TargetID: targetID, Service: key.Service, ContainerPort: key.Port, HostPort: port})
	}
	return store.RememberPorts(ctx, preferences)
}

func requiredEndpointName(key plan.PortKey) string {
	return key.Service + "#" + strconv.Itoa(int(key.Port))
}

type stablePortError struct {
	requirement plan.Requirement
	cause       error
}

func (e *stablePortError) Error() string {
	return fmt.Sprintf("stable host port %d for %s:%d is unavailable", e.requirement.HostPort, e.requirement.Key.Service, e.requirement.Key.Port)
}

func (*stablePortError) Help() string {
	return "stop the process using the port, then retry trocker up"
}

func (*stablePortError) Note() string {
	return "the assignment is retained; Trocker does not choose another port"
}
func (e *stablePortError) Unwrap() error { return e.cause }

type allocationError struct{}

func (*allocationError) Error() string { return "could not allocate workspace publication ports" }
func (*allocationError) Help() string  { return "check local port conflicts and retry trocker up" }
func (*allocationError) Note() string {
	return "partial resources and volumes were preserved; unrelated listeners were not stopped"
}
