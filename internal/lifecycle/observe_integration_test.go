//go:build integration

package lifecycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/cli"
	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestReadOnlyObservations(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	directory, main := repository(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DOCKER_CONFIG") == "" {
		t.Setenv("DOCKER_CONFIG", filepath.Join(home, ".docker"))
	}
	t.Setenv("HOME", directory)
	t.Setenv("XDG_STATE_HOME", directory)
	t.Setenv("LOCALAPPDATA", directory)
	stateDirectory, err := state.DefaultDirectory()
	if err != nil {
		t.Fatal(err)
	}
	store := state.Store{Directory: stateDirectory}
	for name, content := range map[string]string{
		"compose.yaml": "services:\n  api:\n    image: busybox:1.37\n    environment: {SECRET: do-not-print}\n    expose: ['8000']\n  missing:\n    image: busybox:1.37\n",
		"trocker.yaml": "version: 1\ncompose: [compose.yaml]\nendpoints:\n  api: {service: api, port: 8000, scheme: http}\n",
	} {
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
		if err := target.Close(); err != nil {
			t.Error(err)
		}
	})
	pull, err := target.Client.ImagePull(ctx, "busybox:1.37", client.ImagePullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := pull.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pull.Close(); err != nil {
		t.Fatal(err)
	}
	owned := createObservationContainer(t, ctx, target, registration.ProjectName(), registration.ID, true)
	other := workspace.Registration{ID: uuid.NewString()}
	createObservationContainer(t, ctx, target, other.ProjectName(), other.ID, false)
	before := snapshotObservationFiles(t, main, store.Directory)
	if _, err := target.Client.ContainerStart(ctx, owned, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	// An unhealthy listener still has a published mapping; readiness must not be inferred.
	for {
		inspected, err := target.Client.ContainerInspect(ctx, owned, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if inspected.Container.State.Health.Status == container.Unhealthy {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	observation, err := lifecycle.Observe(ctx, main, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Services) != 2 || observation.Services[0].Health != "unhealthy" || observation.Services[1].State != "missing" {
		t.Fatalf("unexpected services: %+v", observation.Services)
	}
	endpoints, err := lifecycle.Endpoints(ctx, main, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints.Endpoints) != 1 || endpoints.Endpoints[0].State != "published" || endpoints.Endpoints[0].Addresses.Host.Port == 0 {
		t.Fatalf("endpoints=%+v", endpoints)
	}
	t.Run("CLI output", func(t *testing.T) {
		t.Chdir(main)
		var output, diagnostics bytes.Buffer
		if err := cli.RunContext(ctx, []string{"endpoints", "--json"}, "test", strings.NewReader(""), &output, &diagnostics); err != nil {
			t.Fatal(err)
		}
		var result lifecycle.EndpointResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.SchemaVersion != 1 || result.WorkspaceID != registration.ID || len(result.Endpoints) != 1 || result.Endpoints[0].State != "published" || diagnostics.Len() != 0 {
			t.Fatalf("unexpected CLI result: %s", &output)
		}
		for _, name := range []string{"plan", "doctor"} {
			output.Reset()
			if err := cli.RunContext(ctx, []string{name}, "test", strings.NewReader(""), &output, &diagnostics); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "No resources were changed") || strings.Contains(output.String(), "do-not-print") {
				t.Fatalf("unsafe plan output: %s", &output)
			}
		}
		output.Reset()
		if err := cli.RunContext(ctx, []string{"status"}, "test", strings.NewReader(""), &output, &diagnostics); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "unhealthy") || !strings.Contains(output.String(), "missing") {
			t.Fatalf("incomplete status: %s", &output)
		}
	})
	timeout := 0
	if _, err := target.Client.ContainerStop(ctx, owned, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
		t.Fatal(err)
	}
	endpoints, err = lifecycle.Endpoints(ctx, main, store)
	if err != nil {
		t.Fatal(err)
	}
	if endpoints.Endpoints[0].State != "stopped" || endpoints.Endpoints[0].Addresses.Host != nil {
		t.Fatal("stopped container retained a live address")
	}
	after := snapshotObservationFiles(t, main, store.Directory)
	if len(before) != len(after) {
		t.Fatal("observation created files")
	}
	for path, content := range before {
		if !bytes.Equal(content, after[path]) {
			t.Fatalf("observation changed %s", path)
		}
	}
	// This fixture deliberately claims the right project but a different workspace.
	conflicting := registration
	conflicting.ID = uuid.NewString()
	createObservationContainer(t, ctx, target, registration.ProjectName(), conflicting.ID, false)
	if _, err := lifecycle.Observe(ctx, main, store); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("foreign resource accepted: %v", err)
	}
	if _, err := lifecycle.Endpoints(ctx, main, store); err == nil {
		t.Fatal("foreign endpoint accepted")
	}
}

func createObservationContainer(t *testing.T, ctx context.Context, target *compose.Target, project, id string, health bool) string {
	t.Helper()
	token := uuid.NewString()
	port, err := network.ParsePort("8000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	config := &container.Config{Image: "busybox:1.37", Cmd: []string{"sleep", "300"}, Labels: map[string]string{
		api.ProjectLabel: project, api.ServiceLabel: "api", api.ConfigHashLabel: "test", compose.WorkspaceLabel: id, "io.trocker.test-id": token,
	}, ExposedPorts: network.PortSet{port: {}}}
	if health {
		config.Healthcheck = &container.HealthConfig{Test: []string{"CMD", "false"}, Interval: time.Second, Timeout: time.Second, Retries: 1}
	}
	created, err := target.Client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: "trocker-test-" + token, Config: config,
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		inspected, err := target.Client.ContainerInspect(cleanup, created.ID, client.ContainerInspectOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		if inspected.Container.Config.Labels["io.trocker.test-id"] != token {
			t.Error("refusing cleanup of unverified fixture")
			return
		}
		if _, err := target.Client.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Error(err)
		}
	})
	return created.ID
}

func snapshotObservationFiles(t *testing.T, paths ...string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			content, err := os.ReadFile(path) //nolint:gosec // Snapshot only the test-owned checkout and registry.
			if err != nil {
				return err
			}
			files[path] = content
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestQueryFailureHasNoJSONOutput(t *testing.T) {
	directory, main := repository(t)
	t.Chdir(main)
	t.Setenv("XDG_STATE_HOME", filepath.Join(directory, "state"))
	t.Setenv("HOME", directory)
	t.Setenv("LOCALAPPDATA", directory)
	var output, diagnostics bytes.Buffer
	err := cli.RunContext(t.Context(), []string{"endpoints", "--json"}, "test", strings.NewReader(""), &output, &diagnostics)
	if err == nil || output.Len() != 0 {
		t.Fatalf("failed query emitted JSON: %q, %v", &output, err)
	}
	if err := cli.WriteError(&diagnostics, err); err != nil {
		t.Fatal(err)
	}
}
