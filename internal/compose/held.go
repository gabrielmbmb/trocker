package compose

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// Held counts the resources on one target that still carry a workspace's
// identity. Callers build it from the observations they already need.
type Held struct {
	Containers int
	Networks   int
	Volumes    int
}

func (h Held) Empty() bool { return h.Containers == 0 && h.Networks == 0 && h.Volumes == 0 }

// Summary describes the counts for diagnostics, in container, network, volume order.
func (h Held) Summary() string {
	parts := make([]string, 0, 3)
	for _, item := range []struct {
		count    int
		singular string
		plural   string
	}{
		{h.Containers, "container", "containers"},
		{h.Networks, "network", "networks"},
		{h.Volumes, "volume", "volumes"},
	} {
		switch {
		case item.count == 1:
			parts = append(parts, "1 "+item.singular)
		case item.count > 1:
			parts = append(parts, fmt.Sprintf("%d %s", item.count, item.plural))
		}
	}
	if len(parts) == 0 {
		return "no resources"
	}
	return strings.Join(parts, ", ")
}

// Retained counts the labelled networks and volumes a registration still owns.
// Containers are observed separately because callers also need their states.
// Counts use the same project label as the lifecycle ownership checks, so a
// conflicting workspace label still counts: refusing to release is safer than
// releasing something whose ownership is unclear.
func (t *Target) Retained(ctx context.Context, registration workspace.Registration) (networks, volumes int, err error) {
	filter := client.Filters{}.Add("label", api.ProjectLabel+"="+registration.ProjectName())
	foundNetworks, err := t.Client.NetworkList(ctx, client.NetworkListOptions{Filters: filter})
	if err != nil {
		return 0, 0, &observationError{err}
	}
	foundVolumes, err := t.Client.VolumeList(ctx, client.VolumeListOptions{Filters: filter})
	if err != nil {
		return 0, 0, &observationError{err}
	}
	return len(foundNetworks.Items), len(foundVolumes.Items), nil
}
