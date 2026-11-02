package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type Observation struct {
	WorkspaceID         string
	TargetID            string
	Services            []ServiceStatus
	containers          []api.ContainerSummary
	endpointDefinitions map[string]config.Endpoint
}

type ServiceStatus struct {
	Service   string
	Container string
	State     string
	Health    string
}

// Observe only reads registration, configuration, and Docker state. It does not
// acquire write locks or repair incomplete/moved registrations.
func Observe(ctx context.Context, directory string, store state.Store) (observation Observation, err error) {
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return observation, err
	}
	manifest, project, err := compose.Load(ctx, filepath.Join(registration.Checkout.Root, "trocker.yaml"))
	if err != nil {
		return observation, err
	}
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		return observation, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	if !slices.Contains(registration.Targets, target.ID) {
		return observation, &registrationError{problem: "workspace is not registered with the selected Docker target"}
	}
	containers, err := target.Containers(ctx, registration)
	if err != nil {
		return observation, err
	}
	return Observation{
		WorkspaceID: registration.ID, TargetID: target.ID,
		Services:   summarizeServices(project.ServiceNames(), containers),
		containers: containers, endpointDefinitions: manifest.Endpoints,
	}, nil
}

func summarizeServices(names []string, containers []api.ContainerSummary) []ServiceStatus {
	services := make([]ServiceStatus, 0, len(containers)+len(names))
	seen := make(map[string]bool)
	for _, ctr := range containers {
		services = append(services, ServiceStatus{Service: ctr.Service, Container: ctr.Name, State: string(ctr.State), Health: string(ctr.Health)})
		if !strings.EqualFold(ctr.Labels[api.OneoffLabel], "true") {
			seen[ctr.Service] = true
		}
	}
	for _, name := range names {
		if !seen[name] {
			services = append(services, ServiceStatus{Service: name, State: "missing"})
		}
	}
	sort.Slice(services, func(i, j int) bool {
		a, b := services[i], services[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Container < b.Container
	})
	return services
}

func registeredWorkspace(ctx context.Context, directory string, store state.Store) (workspace.Registration, error) {
	checkout, err := workspace.Discover(ctx, directory)
	if err != nil {
		return workspace.Registration{}, err
	}
	registry, err := store.Read()
	if err != nil {
		return workspace.Registration{}, err
	}
	index, moved, err := workspace.Find(ctx, checkout, registry.Workspaces)
	if err != nil {
		return workspace.Registration{}, err
	}
	if index < 0 {
		return workspace.Registration{}, &registrationError{problem: "workspace is not registered"}
	}
	if moved {
		return workspace.Registration{}, &registrationError{problem: "workspace moved since registration"}
	}
	registration := registry.Workspaces[index]
	marker, err := workspace.Identity(checkout)
	if err != nil {
		return workspace.Registration{}, err
	}
	if marker != registration.ID {
		return workspace.Registration{}, &registrationError{problem: "workspace registration is incomplete"}
	}
	return registration, nil
}

type registrationError struct{ problem string }

func (e *registrationError) Error() string { return e.problem }
func (e *registrationError) Help() string {
	return "run trocker init to confirm workspace registration"
}

// Endpoints loads configuration once and resolves addresses solely from current
// owned container observations, never registry port preferences.
func Endpoints(ctx context.Context, directory string, store state.Store) (result EndpointResult, err error) {
	observation, err := Observe(ctx, directory, store)
	if err != nil {
		return result, err
	}
	endpoints, err := ResolveEndpoints(observation.endpointDefinitions, observation.containers)
	if err != nil {
		return result, fmt.Errorf("cannot resolve endpoints: %w", err)
	}
	return EndpointResult{SchemaVersion: 1, WorkspaceID: observation.WorkspaceID, TargetID: observation.TargetID, Endpoints: endpoints}, nil
}
