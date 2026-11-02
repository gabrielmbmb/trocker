package compose

import (
	"maps"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestLifecycleRejectsUnsafeContainerMetadata(t *testing.T) {
	t.Parallel()
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	labels := map[string]string{WorkspaceLabel: registration.ID, api.ServiceLabel: "api", api.ConfigHashLabel: "hash", api.OneoffLabel: "False"}
	for _, test := range []struct{ name, key, value string }{
		{"foreign owner", WorkspaceLabel, "other"},
		{"unowned", WorkspaceLabel, ""},
		{"hook without config hash", api.ConfigHashLabel, ""},
		{"missing service", api.ServiceLabel, ""},
		{"missing lifecycle membership", api.OneoffLabel, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := maps.Clone(labels)
			changed[test.key] = test.value
			if err := checkLifecycleContainers([]container.Summary{{ID: "fixture", Labels: changed}}, registration, nil); err == nil {
				t.Fatal("unsafe container accepted")
			}
		})
	}
	if err := checkLifecycleContainers([]container.Summary{{Labels: labels}}, registration, []string{"missing"}); err == nil {
		t.Fatal("missing service accepted")
	}
	if err := checkLifecycleContainers([]container.Summary{{Labels: labels}}, registration, []string{"api"}); err != nil {
		t.Fatal(err)
	}
}
