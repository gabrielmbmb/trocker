//go:build integration

package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

const (
	upCompose = `services:
  api:
    image: busybox:1.37
    stop_grace_period: 1s
    command: [sh, -c, 'test -f /data/token || hostname > /data/token; httpd -f -p 8000 -h /data']
    expose: ['8000']
    volumes: ['data:/data']
    healthcheck:
      test: [CMD, test, -f, /data/token]
      interval: 1s
      timeout: 1s
      retries: 3
volumes:
  data:
`
	upManifest = "version: 1\ncompose: [compose.yaml]\nendpoints:\n  api: {service: api, port: 8000, scheme: http}\n"
)

func upWorkspace(t *testing.T, ctx context.Context) (string, state.Store, workspace.Registration, *compose.Target) {
	t.Helper()
	return workspaceFixture(t, ctx, upCompose, upManifest)
}

// workspaceFixture writes the Compose and manifest files, registers the
// workspace, and schedules cleanup of everything it creates.
func workspaceFixture(t *testing.T, ctx context.Context, composeFile, manifest string) (string, state.Store, workspace.Registration, *compose.Target) {
	t.Helper()
	directory, main := repository(t) //nolint:contextcheck // Git fixtures use the test context; daemon operations use the shorter timeout.
	store := state.Store{Directory: filepath.Join(directory, "state")}
	for name, content := range map[string]string{"compose.yaml": composeFile, "trocker.yaml": manifest} {
		if err := os.WriteFile(filepath.Join(main, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registration, err := lifecycle.Init(ctx, main, store, func(lifecycle.InitProposal) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanupProject(t, cleanup, target, registration)
		if err := target.Close(); err != nil {
			t.Error(err)
		}
	})
	return main, store, registration, target
}

func TestUpRetryIsolationAndVolumeRetention(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	before := snapshotObservationFiles(t, main, store.Directory)
	prepared, err := lifecycle.Plan(ctx, main, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Project.Name != registration.ProjectName() {
		t.Fatal("plan changed identity")
	}
	after := snapshotObservationFiles(t, main, store.Directory)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("plan changed local inputs")
	}
	options := compose.UpOptions{Detach: true}
	var progress bytes.Buffer
	first, err := lifecycle.Up(ctx, main, store, options, &progress)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(progress.String(), registration.ProjectName()+"-api-1") || !strings.Contains(progress.String(), "Created") || strings.Contains(progress.String(), "\x1b") {
		t.Fatalf("missing or decorated plain Compose progress: %q", &progress)
	}
	token := readEndpointToken(t, ctx, first)
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers: %v %v", containers, err)
	}
	id := containers[0].ID
	second, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	containers, err = target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if containers[0].ID != id || second.Endpoints[0].Addresses.Host.Port != first.Endpoints[0].Addresses.Host.Port {
		t.Fatal("unchanged up recreated containers")
	}
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(strings.Replace(upCompose, "    expose:", "    environment: {GENERATION: two}\n    expose:", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	recreated, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	containers, err = target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if containers[0].ID == id || readEndpointToken(t, ctx, recreated) != token {
		t.Fatal("recreation failed to preserve named-volume data")
	}
	other, otherStore, otherRegistration, _ := upWorkspace(t, ctx)
	independent, err := lifecycle.Up(ctx, other, otherStore, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if independent.WorkspaceID == registration.ID || independent.Endpoints[0].Addresses.Host.Port == recreated.Endpoints[0].Addresses.Host.Port || otherRegistration.ProjectName() == registration.ProjectName() {
		t.Fatal("independent workspaces collided")
	}
}

func TestUpBuildAndServiceSelection(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	content := strings.Replace(upCompose, "    image: busybox:1.37", "    build: .", 1)
	content = strings.Replace(content, "volumes:\n  data:", "  other:\n    image: busybox:1.37\n    command: [sleep, '300']\nvolumes:\n  data:", 1)
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "Dockerfile"), []byte("FROM busybox:1.37\nRUN true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true, Build: true, Services: []string{"api"}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	readEndpointToken(t, ctx, result)
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(containers) != 1 || containers[0].Service != "api" {
		t.Fatalf("selection leaked: %v %v", containers, err)
	}
	image, err := target.Client.ImageInspect(ctx, registration.ProjectName()+"-api:latest")
	if err != nil {
		t.Fatal(err)
	}
	if image.Config.Labels[compose.WorkspaceLabel] != registration.ID {
		t.Fatal("build image lacks ownership")
	}
}

func TestProfileGatedServicePlanUpAndDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	const composeContent = `services:
  api:
    image: busybox:1.37
    command: [sleep, '300']
  gated:
    image: busybox:1.37
    command: [sleep, '300']
    expose: ['8000']
    profiles: [extra]
`
	const manifest = "version: 1\ncompose: [compose.yaml]\nprofiles: [extra]\nendpoints:\n  gated: {service: gated, port: 8000, scheme: http}\n"
	main, store, registration, target := workspaceFixture(t, ctx, composeContent, manifest)
	preview, err := lifecycle.Plan(ctx, main, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Project.Services) != 2 || !slices.Equal(preview.Project.Profiles, []string{"extra"}) {
		t.Fatalf("plan profiles/services = %v/%v", preview.Project.Profiles, preview.Project.ServiceNames())
	}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true, Services: []string{"gated"}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints) != 1 || result.Endpoints[0].Name != "gated" || result.Endpoints[0].State != "published" {
		t.Fatalf("profile-gated endpoint = %+v", result.Endpoints)
	}
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(containers) != 1 || containers[0].Service != "gated" {
		t.Fatalf("selected profile-gated service containers = %v, %v", containers, err)
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down}, io.Discard); err != nil {
		t.Fatal(err)
	}
	containers, err = target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(containers) != 0 {
		t.Fatalf("down retained profile-gated containers: %v, %v", containers, err)
	}
}

func TestBuildFailureDoesNotReportSuccessOrLoseRegistration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, _ := upWorkspace(t, ctx)
	writeSelfBinding(t, main)
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(strings.Replace(upCompose, "    image: busybox:1.37", "    build: .", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "Dockerfile"), []byte("DO_NOT_PRINT_SECRET invalid instruction\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true, Build: true}, &progress)
	if strings.Contains(progress.String(), "DO_NOT_PRINT_SECRET") || strings.Contains(progress.String(), "retrying") {
		t.Fatal("progress leaked raw build failure")
	}
	if err == nil || strings.Contains(err.Error(), "DO_NOT_PRINT_SECRET") || result.SchemaVersion != 0 || result.Endpoints != nil {
		t.Fatalf("build failure reported success or leaked input: %v", err)
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].ID != registration.ID {
		t.Fatal("build failure damaged registration")
	}
}

func TestForegroundUpStreamsLogsAndReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	main, store, _, _ := upWorkspace(t, ctx)
	content := strings.Replace(upCompose, "test -f /data/token || hostname > /data/token; httpd -f -p 8000 -h /data", "echo foreground-ready; sleep 1", 1)
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{}, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "foreground-ready") {
		t.Fatalf("foreground output missing: %s", &logs)
	}
}

func TestConcurrentWorkspaceStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	first, firstStore, _, _ := upWorkspace(t, ctx)
	second, secondStore, _, _ := upWorkspace(t, ctx)
	type outcome struct {
		result lifecycle.EndpointResult
		err    error
	}
	ready := make(chan outcome, 2)
	for _, fixture := range []struct {
		directory string
		store     state.Store
	}{{first, firstStore}, {second, secondStore}} {
		go func() {
			result, err := lifecycle.Up(ctx, fixture.directory, fixture.store, compose.UpOptions{Detach: true}, io.Discard)
			ready <- outcome{result, err}
		}()
	}
	a, b := <-ready, <-ready
	if a.err != nil || b.err != nil {
		t.Fatalf("concurrent startup: %v / %v", a.err, b.err)
	}
	if a.result.WorkspaceID == b.result.WorkspaceID || a.result.Endpoints[0].Addresses.Host.Port == b.result.Endpoints[0].Addresses.Host.Port {
		t.Fatal("concurrent workspaces collided")
	}
}

func readEndpointToken(t *testing.T, ctx context.Context, result lifecycle.EndpointResult) string {
	t.Helper()
	if len(result.Endpoints) != 1 || result.Endpoints[0].State != "published" {
		t.Fatalf("not published: %+v", result)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, result.Endpoints[0].Addresses.Host.URL+"/token", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d", response.StatusCode)
	}
	return string(data)
}

func cleanupProject(t *testing.T, ctx context.Context, target *compose.Target, registration workspace.Registration) {
	t.Helper()
	filter := client.Filters{}.Add("label", api.ProjectLabel+"="+registration.ProjectName())
	containers, err := target.Client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		t.Error(err)
		return
	}
	for _, ctr := range containers.Items {
		if ctr.Labels[compose.WorkspaceLabel] != registration.ID {
			t.Error("refusing cleanup of unowned container")
			continue
		}
		if _, err := target.Client.ContainerRemove(ctx, ctr.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Error(err)
		}
	}
	networks, err := target.Client.NetworkList(ctx, client.NetworkListOptions{Filters: filter})
	if err != nil {
		t.Error(err)
		return
	}
	for _, network := range networks.Items {
		if network.Labels[compose.WorkspaceLabel] != registration.ID {
			t.Error("refusing cleanup of unowned network")
			continue
		}
		if _, err := target.Client.NetworkRemove(ctx, network.ID, client.NetworkRemoveOptions{}); err != nil {
			t.Error(err)
		}
	}
	volumes, err := target.Client.VolumeList(ctx, client.VolumeListOptions{Filters: filter})
	if err != nil {
		t.Error(err)
		return
	}
	imageName := registration.ProjectName() + "-api:latest"
	image, imageErr := target.Client.ImageInspect(ctx, imageName)
	if imageErr == nil {
		if image.Config.Labels[compose.WorkspaceLabel] != registration.ID {
			t.Error("refusing cleanup of unowned build image")
		} else if _, err := target.Client.ImageRemove(ctx, imageName, client.ImageRemoveOptions{}); err != nil {
			t.Error(err)
		}
	} else if !errdefs.IsNotFound(imageErr) {
		t.Error(imageErr)
	}
	for _, volume := range volumes.Items {
		if volume.Labels[compose.WorkspaceLabel] != registration.ID {
			t.Error("refusing cleanup of unowned volume")
			continue
		}
		if _, err := target.Client.VolumeRemove(ctx, volume.Name, client.VolumeRemoveOptions{}); err != nil {
			t.Error(err)
		}
	}
}

const hostAccessCompose = `services:
  api:
    image: busybox:1.37
    pid: host
    ipc: host
    cap_add: [SYS_PTRACE]
    devices: ['/dev/null:/dev/null:rw']
    command: [sleep, '300']
`

func TestUpAppliesDeclaredHostAccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	manifest := "version: 1\ncompose: [compose.yaml]\nhost_access:\n  api:\n    pid: true\n    ipc: true\n    capabilities: [SYS_PTRACE]\n    devices:\n      - source: /dev/null\n        permissions: rw\n"
	main, store, registration, target := workspaceFixture(t, ctx, hostAccessCompose, manifest)
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers: %v %v", containers, err)
	}
	inspected, err := target.Client.ContainerInspect(ctx, containers[0].ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	host := inspected.Container.HostConfig
	if host == nil {
		t.Fatal("container has no host config")
	}
	if host.PidMode != "host" || host.IpcMode != "host" {
		t.Fatalf("host namespaces = %q, %q, want host, host", host.PidMode, host.IpcMode)
	}
	found := false
	for _, capability := range host.CapAdd {
		if config.NormalizeCapability(capability) == "SYS_PTRACE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("capabilities = %#v, want SYS_PTRACE", host.CapAdd)
	}
	found = false
	for _, device := range host.Devices {
		if device.PathOnHost == "/dev/null" && device.CgroupPermissions == "rw" {
			found = true
		}
	}
	if !found {
		t.Fatalf("devices = %#v, want /dev/null with rw", host.Devices)
	}
}

func TestUpRejectsForeignRetainedResources(t *testing.T) {
	for _, kind := range []string{"network", "volume"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			main, store, registration, target := upWorkspace(t, ctx)
			labels := map[string]string{"io.trocker.fixture-id": registration.ID}
			name := registration.ProjectName() + "_data"
			if kind == "network" {
				name = registration.ProjectName() + "_default"
				created, err := target.Client.NetworkCreate(ctx, name, client.NetworkCreateOptions{Labels: labels})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
					defer cancel()
					found, err := target.Client.NetworkInspect(cleanup, created.ID, client.NetworkInspectOptions{})
					if err != nil {
						t.Error(err)
						return
					}
					if found.Network.Labels["io.trocker.fixture-id"] != registration.ID || found.Network.Labels[compose.WorkspaceLabel] != "" {
						t.Error("foreign network was adopted or changed")
						return
					}
					if _, err := target.Client.NetworkRemove(cleanup, created.ID, client.NetworkRemoveOptions{}); err != nil {
						t.Error(err)
					}
				})
			} else {
				if _, err := target.Client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
					defer cancel()
					found, err := target.Client.VolumeInspect(cleanup, name, client.VolumeInspectOptions{})
					if err != nil {
						t.Error(err)
						return
					}
					if found.Volume.Labels["io.trocker.fixture-id"] != registration.ID || found.Volume.Labels[compose.WorkspaceLabel] != "" {
						t.Error("foreign volume was adopted or changed")
						return
					}
					if _, err := target.Client.VolumeRemove(cleanup, name, client.VolumeRemoveOptions{}); err != nil {
						t.Error(err)
					}
				})
			}
			if _, err := lifecycle.Plan(ctx, main, store, nil); err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("plan accepted foreign %s: %v", kind, err)
			}
			if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("up accepted foreign %s: %v", kind, err)
			}
			containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
			if err != nil || len(containers) != 0 {
				t.Fatalf("failed preflight created containers: %v", err)
			}
		})
	}
}

func TestCanceledUpPreservesResourcesAndCanRecover(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	writeSelfBinding(t, main)
	unhealthy := strings.Replace(upCompose, "[CMD, test, -f, /data/token]", "[CMD, 'false']", 1)
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(unhealthy), 0o600); err != nil {
		t.Fatal(err)
	}
	startup, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		_, err := lifecycle.Up(startup, main, store, compose.UpOptions{Detach: true}, io.Discard)
		done <- err
	}()
	for {
		containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(containers) > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("startup ended before cancellation: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(containers) == 0 {
		t.Fatal("cancellation deleted partial resources")
	}
	if _, err := target.Client.VolumeInspect(ctx, registration.ProjectName()+"_data", client.VolumeInspectOptions{}); err != nil {
		t.Fatal("cancellation lost volume", err)
	}
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(upCompose), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != registration.ID {
		t.Fatal("recovery changed workspace identity")
	}
	readEndpointToken(t, ctx, result)
}
