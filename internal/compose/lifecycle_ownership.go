package compose

import (
	"context"
	"fmt"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func (t *Target) checkLifecycleOwnership(ctx context.Context, registration workspace.Registration, options LifecycleOptions) error {
	filter := client.Filters{}.Add("label", api.ProjectLabel+"="+registration.ProjectName())
	containers, err := t.Client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return &observationError{err}
	}
	if err := checkLifecycleContainers(containers.Items, registration, options.Services); err != nil {
		return err
	}
	if options.Operation != Down {
		return nil
	}
	if err := t.checkRetainedNetworks(ctx, registration, filter); err != nil {
		return err
	}
	if options.Volumes {
		if err := t.checkRetainedVolumes(ctx, registration, filter); err != nil {
			return err
		}
		return t.checkAttachedVolumes(ctx, registration, containers.Items)
	}
	return nil
}

func checkLifecycleContainers(containers []container.Summary, registration workspace.Registration, services []string) error {
	existing := make(map[string]bool)
	for _, ctr := range containers {
		if ctr.Labels[WorkspaceLabel] != registration.ID {
			return &ownershipError{kind: "container", name: ctr.ID}
		}
		// In particular, don't permit SDK hook cleanup to delete attached volumes
		// during ordinary down. Trocker does not create hook containers.
		oneoff := ctr.Labels[api.OneoffLabel]
		if ctr.Labels[api.ConfigHashLabel] == "" || ctr.Labels[api.ServiceLabel] == "" || (oneoff != "False" && oneoff != "True") {
			return fmt.Errorf("container %s lacks supported Compose lifecycle metadata; explicit recovery required", ctr.ID)
		}
		if oneoff == "False" {
			existing[ctr.Labels[api.ServiceLabel]] = true
		}
	}
	for _, name := range services {
		if !existing[name] {
			return fmt.Errorf("no existing containers for service %s; use trocker up to create it", name)
		}
	}
	return nil
}

func (t *Target) checkRetainedNetworks(ctx context.Context, registration workspace.Registration, filter client.Filters) error {
	networks, err := t.Client.NetworkList(ctx, client.NetworkListOptions{Filters: filter})
	if err != nil {
		return &observationError{err}
	}
	seen := make(map[string]bool)
	for _, network := range networks.Items {
		key := network.Labels[api.NetworkLabel]
		if err := ownedResource("network", network.Labels, registration, api.NetworkLabel, key); err != nil {
			return err
		}
		if key == "" || seen[key] {
			return fmt.Errorf("workspace network associations are missing or ambiguous; explicit recovery required")
		}
		seen[key] = true
		if err := t.checkNetworkContainers(ctx, network.ID, registration); err != nil {
			return err
		}
	}
	return nil
}

func (t *Target) checkRetainedVolumes(ctx context.Context, registration workspace.Registration, filter client.Filters) error {
	volumes, err := t.Client.VolumeList(ctx, client.VolumeListOptions{Filters: filter})
	if err != nil {
		return &observationError{err}
	}
	seen := make(map[string]bool)
	for _, volume := range volumes.Items {
		key := volume.Labels[api.VolumeLabel]
		if err := ownedResource("volume", volume.Labels, registration, api.VolumeLabel, key); err != nil {
			return err
		}
		if key == "" || seen[key] {
			return fmt.Errorf("workspace volume associations are missing or ambiguous; explicit recovery required")
		}
		seen[key] = true
	}
	return nil
}

// Engine RemoveVolumes removes anonymous mounts, not explicitly named external
// volumes. Delegate that distinction to Engine, but reject mounts claiming a
// different owner before allowing any removal. Unlabelled anonymous volumes
// are associated through their attachment to a verified workspace container.
func (t *Target) checkAttachedVolumes(ctx context.Context, registration workspace.Registration, containers []container.Summary) error {
	for _, ctr := range containers {
		for _, attached := range ctr.Mounts {
			if attached.Type != mount.TypeVolume {
				continue
			}
			found, err := t.Client.VolumeInspect(ctx, attached.Name, client.VolumeInspectOptions{})
			if err != nil {
				return &observationError{err}
			}
			labels := found.Volume.Labels
			if owner := labels[WorkspaceLabel]; owner != "" && owner != registration.ID {
				return &ownershipError{kind: "volume", name: attached.Name}
			}
			if project := labels[api.ProjectLabel]; project != "" && project != registration.ProjectName() {
				return &ownershipError{kind: "volume", name: attached.Name}
			}
		}
	}
	return nil
}
