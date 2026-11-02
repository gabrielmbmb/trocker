//go:build integration

package lifecycle_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

const upCompose = `services:
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

func upWorkspace(t *testing.T, ctx context.Context) (string, state.Store, workspace.Registration, *compose.Target) {
	t.Helper()
	directory, main := repository(t) //nolint:contextcheck // Git fixtures use the test context; daemon operations use the shorter timeout.
	store := state.Store{Directory: filepath.Join(directory, "state")}
	for name, content := range map[string]string{"compose.yaml": upCompose, "trocker.yaml": "version: 1\ncompose: [compose.yaml]\nendpoints:\n  api: {service: api, port: 8000, scheme: http}\n"} {
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
	prepared, err := lifecycle.Plan(ctx, main, store)
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
	containers, err := target.Containers(ctx, registration)
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers: %v %v", containers, err)
	}
	id := containers[0].ID
	second, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	containers, err = target.Containers(ctx, registration)
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
	containers, err = target.Containers(ctx, registration)
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
	containers, err := target.Containers(ctx, registration)
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
