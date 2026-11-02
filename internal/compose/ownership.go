package compose

import (
	"context"
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/containerd/errdefs"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// CheckOwnership covers all project-labelled containers (including those Ps
// excludes), and exact resource names Compose might reuse during convergence.
func (t *Target) CheckOwnership(ctx context.Context, project *types.Project, registration workspace.Registration) error {
	if err := t.checkProjectContainers(ctx, registration); err != nil {
		return err
	}
	for key, network := range project.Networks {
		if err := t.checkNetwork(ctx, network.Name, key, registration); err != nil {
			return err
		}
	}
	for key, volume := range project.Volumes {
		if err := t.checkVolume(ctx, volume.Name, key, registration); err != nil {
			return err
		}
	}
	for _, service := range project.Services {
		if service.Build != nil {
			if err := t.checkBuildImage(ctx, service.Image, registration.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Target) checkProjectContainers(ctx context.Context, registration workspace.Registration) error {
	containers, err := t.Client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", api.ProjectLabel+"="+registration.ProjectName())})
	if err != nil {
		return &observationError{err}
	}
	for _, ctr := range containers.Items {
		if ctr.Labels[WorkspaceLabel] != registration.ID {
			return &ownershipError{kind: "container", name: ctr.ID}
		}
	}
	return nil
}

func (t *Target) checkNetwork(ctx context.Context, name, key string, registration workspace.Registration) error {
	found, err := t.Client.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("name", name)})
	if err != nil {
		return &observationError{err}
	}
	matches := 0
	for _, item := range found.Items {
		if item.Name != name {
			continue
		}
		matches++
		if matches > 1 {
			return fmt.Errorf("network %s has ambiguous duplicate names", key)
		}
		if err := ownedResource("network", item.Labels, registration, api.NetworkLabel, key); err != nil {
			return err
		}
		if err := t.checkNetworkContainers(ctx, item.ID, registration); err != nil {
			return err
		}
	}
	return nil
}

// Compose may recreate a diverged network. Refuse if doing so could affect a
// container outside this workspace, even when the network itself is owned.
func (t *Target) checkNetworkContainers(ctx context.Context, id string, registration workspace.Registration) error {
	inspected, err := t.Client.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		return &observationError{err}
	}
	for id := range inspected.Network.Containers {
		ctr, err := t.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return &observationError{err}
		}
		labels := ctr.Container.Config.Labels
		if labels[WorkspaceLabel] != registration.ID || labels[api.ProjectLabel] != registration.ProjectName() {
			return &ownershipError{kind: "container", name: id}
		}
	}
	return nil
}

func (t *Target) checkVolume(ctx context.Context, name, key string, registration workspace.Registration) error {
	found, err := t.Client.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return &observationError{err}
	}
	return ownedResource("volume", found.Volume.Labels, registration, api.VolumeLabel, key)
}

func (t *Target) checkBuildImage(ctx context.Context, name, id string) error {
	found, err := t.Client.ImageInspect(ctx, name)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return &observationError{err}
	}
	if found.Config == nil || found.Config.Labels[WorkspaceLabel] != id {
		return fmt.Errorf("workspace build tag refers to an image without matching ownership")
	}
	return nil
}

func ownedResource(kind string, labels map[string]string, registration workspace.Registration, key, value string) error {
	if labels[WorkspaceLabel] != registration.ID || labels[api.ProjectLabel] != registration.ProjectName() || labels[key] != value {
		return &ownershipError{kind: kind, name: value}
	}
	return nil
}
