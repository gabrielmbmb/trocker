package state_test

import (
	"path/filepath"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
)

// A port preference whose workspace is gone makes the whole registry invalid,
// so removing an association must always remove its preferences in the same
// update. This is the trap that made hand-editing registry.json unusable.
func TestRemoveTargetKeepsRegistryValid(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store := state.Store{Directory: directory}
	registration, err := lifecycle.Register(t.Context(), store, fakeCheckout(t, directory, "release"), "daemon")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RememberPorts(t.Context(), []state.PortPreference{{WorkspaceID: registration.ID, TargetID: "daemon", Service: "api", ContainerPort: 8000, HostPort: 24001}}); err != nil {
		t.Fatal(err)
	}

	released := false
	if err := store.Update(t.Context(), func(registry *state.Registry) error {
		released = registry.RemoveTarget(registration.ID, "daemon")
		return nil
	}); err != nil {
		t.Fatalf("removal made the registry invalid: %v", err)
	}
	if !released {
		t.Fatal("removing the only target kept the registration")
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 0 || len(registry.Ports) != 0 {
		t.Fatalf("registry after removal = %+v, %v", registry, err)
	}
}

func TestRemoveTargetKeepsOtherTargetsAndTheirPreferences(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store := state.Store{Directory: directory}
	registration, err := lifecycle.Register(t.Context(), store, fakeCheckout(t, directory, "targets"), "daemon")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Register(t.Context(), store, registration.Checkout, "other"); err != nil {
		t.Fatal(err)
	}
	if err := store.RememberPorts(t.Context(), []state.PortPreference{
		{WorkspaceID: registration.ID, TargetID: "daemon", Service: "api", ContainerPort: 8000, HostPort: 24001},
		{WorkspaceID: registration.ID, TargetID: "other", Service: "api", ContainerPort: 8000, HostPort: 24002},
	}); err != nil {
		t.Fatal(err)
	}

	released := true
	if err := store.Update(t.Context(), func(registry *state.Registry) error {
		released = registry.RemoveTarget(registration.ID, "daemon")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("registration removed while another target still depends on it")
	}
	registry, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Workspaces) != 1 || len(registry.Workspaces[0].Targets) != 1 || registry.Workspaces[0].Targets[0] != "other" {
		t.Fatalf("registration = %+v", registry.Workspaces)
	}
	if len(registry.Ports) != 1 || registry.Ports[0].TargetID != "other" {
		t.Fatalf("preferences = %+v", registry.Ports)
	}
}

func TestRemoveTargetIgnoresUnknownRegistration(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store := state.Store{Directory: directory}
	if err := store.Update(t.Context(), func(registry *state.Registry) error {
		if registry.RemoveTarget("00000000-0000-0000-0000-000000000000", "daemon") {
			t.Fatal("unknown registration reported as removed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(); err != nil {
		t.Fatalf("no-op update damaged the registry: %v", err)
	}
}
