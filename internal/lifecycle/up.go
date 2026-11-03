package lifecycle

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"

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
	NativeFiles    int
	ContainerFiles int
}

func preparePlan(ctx context.Context, directory string, store state.Store, services []string) (PlanResult, error) {
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return PlanResult{}, err
	}
	manifest, source, err := compose.LoadForPlan(ctx, filepath.Join(registration.Checkout.Root, "trocker.yaml"))
	if err != nil {
		return PlanResult{}, err
	}
	project, err := plan.Build(manifest, source, registration, plan.Options{Services: services, Preview: true})
	if err != nil {
		return PlanResult{}, err
	}
	required, err := plan.RequiredPorts(manifest, project)
	if err != nil {
		return PlanResult{}, err
	}
	return PlanResult{Registration: registration, Project: project, manifest: manifest, source: source, PlannedPorts: len(required)}, nil
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
func Plan(ctx context.Context, directory string, store state.Store) (result PlanResult, err error) {
	result, err = preparePlan(ctx, directory, store, nil)
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
	if err := target.CheckOwnership(ctx, result.Project, result.Registration); err != nil {
		return PlanResult{}, err
	}
	return result, nil
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
	prepared, err := preparePlan(ctx, directory, store, options.Services)
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
	allocation, err := startPlanned(ctx, store, prepared, target, options, logs)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, allocation.Close()) }()
	if err := target.CheckOwnership(ctx, prepared.Project, prepared.Registration); err != nil {
		return result, err
	}
	containers, err := target.Containers(ctx, prepared.Registration)
	if err != nil {
		return result, err
	}
	definitions := make(map[string]config.Endpoint)
	for name, endpoint := range prepared.manifest.Endpoints {
		if _, ok := prepared.Project.Services[endpoint.Service]; ok {
			definitions[name] = endpoint
		}
	}
	endpoints, err := ResolveEndpoints(definitions, containers)
	if err != nil {
		return result, err
	}
	if err := rememberAllocations(ctx, store, prepared, target.ID, allocation, endpoints, options.Detach); err != nil {
		return result, err
	}
	return EndpointResult{SchemaVersion: 1, WorkspaceID: prepared.Registration.ID, TargetID: target.ID, Endpoints: endpoints}, nil
}
