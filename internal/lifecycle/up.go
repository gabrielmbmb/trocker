package lifecycle

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/generated"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type PlanResult struct {
	Registration   workspace.Registration
	Project        *types.Project
	manifest       *config.Manifest
	source         *types.Project
	PlannedPorts   int
	StablePorts    bool
	Publications   int
	AssignedPorts  int
	EphemeralPorts []PortEntry
	BusyPorts      []PortEntry
	NativeFiles    int
	ContainerFiles int
}

func preparePlan(ctx context.Context, directory string, store state.Store, services, profiles []string) (PlanResult, error) {
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return PlanResult{}, err
	}
	return preparePlanFor(ctx, registration, services, profiles, nil)
}

func preparePlanFor(ctx context.Context, registration workspace.Registration, services, profiles []string, ports map[plan.PortKey]uint16) (PlanResult, error) {
	manifest, source, err := compose.LoadForPlan(ctx, filepath.Join(registration.Checkout.Root, "trocker.yaml"), profiles)
	if err != nil {
		return PlanResult{}, err
	}
	if err := checkStablePortRange(manifest, ephemeralPortRange()); err != nil {
		return PlanResult{}, err
	}
	project, err := plan.Build(manifest, source, registration, plan.Options{Services: services, Ports: ports, Preview: true})
	if err != nil {
		return PlanResult{}, err
	}
	result := PlanResult{Registration: registration, Project: project, manifest: manifest, source: source, StablePorts: manifest.StablePorts}
	if manifest.StablePorts {
		result.Publications, result.AssignedPorts = publicationCounts(manifest, project, ports)
		return result, nil
	}
	required, err := plan.RequiredPorts(manifest, project)
	if err != nil {
		return PlanResult{}, err
	}
	result.PlannedPorts = len(required)
	return result, nil
}

func registeredTarget(ctx context.Context, registration workspace.Registration) (*compose.Target, error) {
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(registration.Targets, target.ID) {
		return nil, errors.Join(&registrationError{problem: "workspace is not registered with the selected Docker target"}, target.Close())
	}
	return target, nil
}

// Plan checks the same isolation and ownership rules as Up without acquiring
// write locks, claiming ports, rewriting metadata, or creating resources.
func Plan(ctx context.Context, directory string, store state.Store, profiles []string) (result PlanResult, err error) {
	result, err = preparePlan(ctx, directory, store, nil, profiles)
	if err != nil {
		return result, err
	}
	counts, err := generated.Validate(ctx, result.manifest)
	result.NativeFiles = counts.Native
	result.ContainerFiles = counts.Container
	if err != nil {
		return PlanResult{}, err
	}
	target, err := registeredTarget(ctx, result.Registration)
	if err != nil {
		return PlanResult{}, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	registry, err := store.Read()
	if err != nil {
		return PlanResult{}, err
	}
	assignments := assignmentMap(registry, result.Registration.ID, target.ID)
	result, err = applyAssignments(result, assignments)
	if err != nil {
		return PlanResult{}, err
	}
	if err := result.checkAssignments(ctx, target, assignments); err != nil {
		return PlanResult{}, err
	}
	if err := target.CheckOwnership(ctx, result.Project, result.Registration); err != nil {
		return PlanResult{}, err
	}
	return result, nil
}

func applyAssignments(result PlanResult, assignments map[plan.PortKey]uint16) (PlanResult, error) {
	if !result.StablePorts || len(assignments) == 0 {
		return result, nil
	}
	project, err := plan.Build(result.manifest, result.source, result.Registration, plan.Options{Ports: assignments, Preview: true})
	if err != nil {
		return PlanResult{}, err
	}
	result.Project = project
	_, result.AssignedPorts = publicationCounts(result.manifest, project, assignments)
	return result, nil
}

// checkAssignments reports selected stable assignments that would make up fail
// or that other programs could take, without claiming any port.
func (r *PlanResult) checkAssignments(ctx context.Context, target *compose.Target, assignments map[plan.PortKey]uint16) error {
	if !r.StablePorts {
		return nil
	}
	keys := slices.DeleteFunc(plan.Publications(r.manifest, r.Project), func(key plan.PortKey) bool { return assignments[key] == 0 })
	published, err := observedPorts(ctx, target, r.Registration, keys)
	if err != nil {
		return err
	}
	available := func(port uint16) (bool, error) { return portAvailable(ctx, port) }
	r.EphemeralPorts, r.BusyPorts, err = classifyAssignments(keys, assignments, published, ephemeralPortRange(), available)
	return err
}

func publicationCounts(manifest *config.Manifest, project *types.Project, ports map[plan.PortKey]uint16) (publications, assigned int) {
	keys := plan.Publications(manifest, project)
	for _, key := range keys {
		if ports[key] != 0 {
			assigned++
		}
	}
	return len(keys), assigned
}

func Up(ctx context.Context, directory string, store state.Store, options compose.UpOptions, logs io.Writer) (result EndpointResult, err error) {
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return result, err
	}
	lock, err := store.LockWorkspace(ctx, registration.Checkout.GitDir)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	// Reload under the workspace lock: a concurrent init/up may have completed.
	prepared, err := preparePlan(ctx, directory, store, options.Services, options.Profiles)
	if err != nil {
		return result, err
	}
	target, err := registeredTarget(ctx, prepared.Registration)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	if err := target.CheckOwnership(ctx, prepared.Project, prepared.Registration); err != nil {
		return result, err
	}
	stable, err := ensureStableAssignments(ctx, store, prepared, target)
	if err != nil {
		return result, err
	}
	allocation, err := startPlanned(ctx, store, prepared, target, options, stable, logs)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, allocation.Close()) }()
	if err := target.CheckOwnership(ctx, prepared.Project, prepared.Registration); err != nil {
		return result, err
	}
	containers, err := target.Containers(ctx, prepared.Registration, api.PsOptions{All: true})
	if err != nil {
		return result, err
	}
	definitions := make(map[string]config.Endpoint)
	reported := make(map[string]bool)
	for name, endpoint := range prepared.manifest.Endpoints {
		if _, ok := prepared.Project.Services[endpoint.Service]; ok {
			definitions[name] = endpoint
			reported[name] = true
		}
	}
	// Stable publications need not be named endpoints. Observe every allocation
	// so a remembered mapping is verified against a real publication.
	for key := range allocation.ports {
		definitions[requiredEndpointName(key)] = config.Endpoint{Service: key.Service, Port: key.Port, Scheme: "tcp"}
	}
	endpoints, err := ResolveEndpoints(definitions, containers)
	if err != nil {
		return result, err
	}
	if err := rememberAllocations(ctx, store, prepared, target.ID, allocation, endpoints, options.Detach); err != nil {
		return result, err
	}
	visible := make([]Endpoint, 0, len(reported))
	for _, endpoint := range endpoints {
		if reported[endpoint.Name] {
			visible = append(visible, endpoint)
		}
	}
	return EndpointResult{SchemaVersion: 1, WorkspaceID: prepared.Registration.ID, TargetID: target.ID, Endpoints: visible}, nil
}
