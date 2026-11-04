package lifecycle

import (
	"cmp"
	"context"
	"errors"
	"io"
	"slices"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type ContainerResult struct {
	SchemaVersion int             `json:"schema_version"`
	WorkspaceID   string          `json:"workspace_id"`
	Project       string          `json:"project"`
	Containers    []ServiceStatus `json:"containers"`
}

// Containers lists existing workspace containers. It reads Docker state only:
// current Compose and manifest files are not required to inspect the stack.
func Containers(ctx context.Context, directory string, store state.Store, services []string, all bool) (result ContainerResult, err error) {
	registration, target, containers, err := ownedContainers(ctx, directory, store, api.PsOptions{All: all, Services: services})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	return summarizeContainers(registration, containers), nil
}

// Logs reads current container logs without acquiring a workspace lock.
func Logs(ctx context.Context, directory string, store state.Store, options compose.LogOptions, output io.Writer) (err error) {
	registration, target, containers, err := ownedContainers(ctx, directory, store, api.PsOptions{All: true})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	return target.Logs(ctx, registration, containers, options, output)
}

// Exec runs a command in a running service container without acquiring a
// workspace lock. The returned status is the container command's exit code.
func Exec(ctx context.Context, directory string, store state.Store, options compose.ExecOptions, streams compose.ExecStreams) (code int, err error) {
	registration, target, containers, err := ownedContainers(ctx, directory, store, api.PsOptions{All: true})
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	return target.Exec(ctx, registration, containers, options, streams)
}

func ownedContainers(ctx context.Context, directory string, store state.Store, options api.PsOptions) (workspace.Registration, *compose.Target, []api.ContainerSummary, error) {
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return workspace.Registration{}, nil, nil, err
	}
	target, err := registeredTarget(ctx, registration)
	if err != nil {
		return workspace.Registration{}, nil, nil, err
	}
	containers, err := target.Containers(ctx, registration, options)
	if err != nil {
		return workspace.Registration{}, nil, nil, errors.Join(err, target.Close())
	}
	return registration, target, containers, nil
}

func summarizeContainers(registration workspace.Registration, containers []api.ContainerSummary) ContainerResult {
	services := make([]ServiceStatus, 0, len(containers))
	for _, summary := range containers {
		services = append(services, ServiceStatus{
			Service: summary.Service, Container: summary.Name,
			State: string(summary.State), Health: string(summary.Health),
		})
	}
	slices.SortFunc(services, func(a, b ServiceStatus) int { return cmp.Compare(a.Container, b.Container) })
	return ContainerResult{
		SchemaVersion: 1, WorkspaceID: registration.ID,
		Project: registration.ProjectName(), Containers: services,
	}
}
