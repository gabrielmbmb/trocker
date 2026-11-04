//go:build integration

package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestLifecyclePreservesIdentityAndVolumeData(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	first, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	token := readEndpointToken(t, ctx, first)
	before, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	original := containers[0]
	originalInspect, err := target.Client.ContainerInspect(ctx, original.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Stop, Services: []string{"api"}}, &progress); err != nil {
		t.Fatal(err)
	}
	stopped, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0].ID != original.ID || stopped[0].State != container.StateExited {
		t.Fatalf("stop removed or failed to stop container %s: observed %+v; progress: %s", original.ID, stopped, &progress)
	}
	// Existing-resource commands must not depend on a valid current Compose file.
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte("invalid: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Start, Services: []string{"api"}}, &progress); err != nil {
		t.Fatal(err)
	}
	started, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	restartedInspect, err := target.Client.ContainerInspect(ctx, started[0].ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if started[0].ID != original.ID || !reflect.DeepEqual(originalInspect.Container.HostConfig.PortBindings, restartedInspect.Container.HostConfig.PortBindings) {
		t.Fatal("start recreated the container or changed its configured publications")
	}
	if err := os.Remove(filepath.Join(main, "trocker.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down}, &progress); err != nil {
		t.Fatal(err)
	}
	remaining, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(remaining) != 0 {
		t.Fatal("down retained containers", err)
	}
	networks, err := target.Client.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("label", api.ProjectLabel+"="+registration.ProjectName())})
	if err != nil || len(networks.Items) != 0 {
		t.Fatal("down retained networks", err)
	}
	if _, err := target.Client.VolumeInspect(ctx, registration.ProjectName()+"_data", client.VolumeInspectOptions{}); err != nil {
		t.Fatal("down lost named volume", err)
	}
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(upCompose), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "version: 1\ncompose: [compose.yaml]\nendpoints:\n  api: {service: api, port: 8000, scheme: http}\n"
	if err := os.WriteFile(filepath.Join(main, "trocker.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	recreated, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if readEndpointToken(t, ctx, recreated) != token {
		t.Fatal("up after down lost persistent data")
	}
	for range 2 {
		if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down, Volumes: true}, &progress); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := target.Client.VolumeInspect(ctx, registration.ProjectName()+"_data", client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("down --volumes retained managed volume: %v", err)
	}
	after, err := store.Read()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("lifecycle changed registration", err)
	}
	for _, status := range []string{"Stopped", "Started", "Removed"} {
		if !strings.Contains(progress.String(), status) {
			t.Fatalf("native progress missing %s: %s", status, &progress)
		}
	}
	if strings.Contains(progress.String(), "\x1b") {
		t.Fatal("plain lifecycle output contains terminal escapes")
	}
}

func TestLifecycleSelectionAndExternalResourceRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	externalName := registration.ProjectName() + "_external"
	if _, err := target.Client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: externalName, Labels: map[string]string{"io.trocker.fixture-id": registration.ID}}); err != nil {
		t.Fatal(err)
	}
	// Registered before the sidecar cleanup, so mounted volumes are released first.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		volume, err := target.Client.VolumeInspect(cleanup, externalName, client.VolumeInspectOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		if volume.Volume.Labels["io.trocker.fixture-id"] != registration.ID {
			t.Error("refusing unverified volume cleanup")
			return
		}
		if _, err := target.Client.VolumeRemove(cleanup, externalName, client.VolumeRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	bindPath := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(bindPath, []byte("keep this file"), 0o600); err != nil {
		t.Fatal(err)
	}
	timeout := 1
	created, err := target.Client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: "busybox:1.37", Cmd: []string{"sleep", "300"}, StopTimeout: &timeout,
		Labels: map[string]string{api.ProjectLabel: registration.ProjectName(), api.ServiceLabel: "sidecar", api.ConfigHashLabel: "fixture", api.OneoffLabel: "False", api.ContainerNumberLabel: "1", compose.WorkspaceLabel: registration.ID},
	}, HostConfig: &container.HostConfig{Mounts: []mount.Mount{
		{Type: mount.TypeVolume, Source: externalName, Target: "/external"},
		{Type: mount.TypeVolume, Target: "/anonymous"},
		{Type: mount.TypeBind, Source: bindPath, Target: "/marker", ReadOnly: true},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		inspected, err := target.Client.ContainerInspect(cleanup, created.ID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		if inspected.Container.Config.Labels[compose.WorkspaceLabel] != registration.ID {
			t.Error("refusing unverified container cleanup")
			return
		}
		if _, err := target.Client.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Error(err)
		}
	})
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Start, Services: []string{"sidecar"}}, io.Discard); err != nil {
		var wrapped interface{ Unwrap() error }
		if errors.As(err, &wrapped) {
			t.Fatalf("start fixture: %v (cause: %v)", err, wrapped.Unwrap())
		}
		t.Fatal(err)
	}
	inspected, err := target.Client.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	anonymous := ""
	for _, attached := range inspected.Container.Mounts {
		if attached.Destination == "/anonymous" {
			anonymous = attached.Name
		}
	}
	if anonymous == "" {
		t.Fatal("fixture did not get an anonymous volume")
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Stop, Services: []string{"api"}}, io.Discard); err != nil {
		t.Fatal(err)
	}
	sidecar, err := target.Client.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil || sidecar.Container.State.Status != container.StateRunning {
		t.Fatal("selected stop affected unrelated service", err)
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down, Volumes: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Client.VolumeInspect(ctx, anonymous, client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("attached anonymous volume retained: %v", err)
	}
	if _, err := target.Client.VolumeInspect(ctx, externalName, client.VolumeInspectOptions{}); err != nil {
		t.Fatal("external named volume removed", err)
	}
	data, err := os.ReadFile(bindPath) //nolint:gosec // Verify the test-owned bind fixture is unchanged.
	if err != nil || string(data) != "keep this file" {
		t.Fatal("bind-mounted file changed", err)
	}
	if _, err := target.Client.ImageInspect(ctx, "busybox:1.37"); err != nil {
		t.Fatal("image removed", err)
	}
}

func TestStartFixedPortConflictDoesNotReassign(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	other, otherStore, otherRegistration, otherTarget := upWorkspace(t, ctx)
	occupied, err := lifecycle.Up(ctx, other, otherStore, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	main, store, registration, target := upWorkspace(t, ctx)
	port, err := network.ParsePort("8000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	binding := network.PortBinding{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: strconv.Itoa(int(occupied.Endpoints[0].Addresses.Host.Port))}
	created, err := target.Client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: "busybox:1.37", Cmd: []string{"sleep", "300"}, ExposedPorts: network.PortSet{port: {}},
		Labels: map[string]string{api.ProjectLabel: registration.ProjectName(), api.ServiceLabel: "api", api.ConfigHashLabel: "fixture", api.OneoffLabel: "False", api.ContainerNumberLabel: "1", compose.WorkspaceLabel: registration.ID},
	}, HostConfig: &container.HostConfig{PortBindings: network.PortMap{port: {binding}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Start}, io.Discard); err == nil {
		t.Fatal("fixed port conflict was silently reassigned")
	}
	inspected, err := target.Client.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Container.State.Status == container.StateRunning || inspected.Container.State.Error == "" || !reflect.DeepEqual(inspected.Container.HostConfig.PortBindings[port], []network.PortBinding{binding}) {
		t.Fatal("failed start changed configured binding")
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down, Volumes: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	untouched, err := otherTarget.Containers(ctx, otherRegistration, api.PsOptions{All: true})
	if err != nil || len(untouched) != 1 || untouched[0].State != container.StateRunning {
		t.Fatal("teardown affected another project", err)
	}
	readEndpointToken(t, ctx, occupied)
}

func TestCanceledDownCanBeRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	stopping, interrupt := context.WithCancel(ctx)
	defer interrupt()
	err := lifecycle.ApplyLifecycle(stopping, main, store, compose.LifecycleOptions{Operation: compose.Down, Volumes: true}, cancelOnStopping{interrupt})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("down cancellation: %v", err)
	}
	if _, err := target.Client.VolumeInspect(ctx, registration.ProjectName()+"_data", client.VolumeInspectOptions{}); err != nil {
		t.Fatal("canceled down lost volume", err)
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down}, io.Discard); err != nil {
		t.Fatal("retry failed or lock leaked", err)
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].ID != registration.ID {
		t.Fatal("canceled teardown changed registration", err)
	}
}

type cancelOnStopping struct{ cancel context.CancelFunc }

func (w cancelOnStopping) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "Stopping") {
		w.cancel()
	}
	return len(data), nil
}

func TestDownVolumeOwnershipPreflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	name := registration.ProjectName() + "_foreign"
	if _, err := target.Client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: map[string]string{api.ProjectLabel: registration.ProjectName(), api.VolumeLabel: "foreign", "io.trocker.fixture-id": registration.ID}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		volume, err := target.Client.VolumeInspect(cleanup, name, client.VolumeInspectOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		if volume.Volume.Labels["io.trocker.fixture-id"] != registration.ID {
			t.Error("refusing unverified fixture cleanup")
			return
		}
		if _, err := target.Client.VolumeRemove(cleanup, name, client.VolumeRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down, Volumes: true}, io.Discard); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("foreign volume accepted: %v", err)
	}
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(containers) != 1 || containers[0].State != container.StateRunning {
		t.Fatal("ownership failure mutated containers", err)
	}
	// Ordinary down retains every volume, including the foreign labelled fixture.
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Client.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err != nil {
		t.Fatal("default down removed foreign volume", err)
	}
}
