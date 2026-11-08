//go:build integration

package lifecycle_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

const selfBindingManifest = `version: 1
compose: [compose.yaml]
endpoints:
  api: {service: api, port: 8000, scheme: http}
bindings:
  api:
    environment:
      SELF_URL: "{{ endpoints.api.host.url }}"
`

func writeSelfBinding(t *testing.T, main string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(main, "trocker.yaml"), []byte(selfBindingManifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostBindingsReuseAndPreferenceConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	web := `  web:
    image: busybox:1.37
    stop_grace_period: 1s
    depends_on: {api: {condition: service_healthy}}
    command: [sh, -c, 'mkdir -p /www; printf "%s" "$$BROWSER_API_URL" > /www/url; httpd -f -p 8080 -h /www']
    expose: ['8080']
    healthcheck:
      test: [CMD, test, -s, /www/url]
      interval: 1s
      timeout: 1s
      retries: 3
`
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte(strings.Replace(upCompose, "volumes:\n  data:", web+"volumes:\n  data:", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := strings.Replace(selfBindingManifest, "bindings:", "  web: {service: web, port: 8080, scheme: http}\nbindings:", 1) + "  web:\n    environment:\n      BROWSER_API_URL: '{{ endpoints.api.host.url }}'\n"
	if err := os.WriteFile(filepath.Join(main, "trocker.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotObservationFiles(t, main, store.Directory)
	preview, err := lifecycle.Plan(ctx, main, store, nil)
	if err != nil || preview.PlannedPorts != 1 {
		t.Fatal("preview", err)
	}
	if !reflect.DeepEqual(before, snapshotObservationFiles(t, main, store.Directory)) {
		t.Fatal("plan mutated inputs or state")
	}
	options := compose.UpOptions{Detach: true}
	first, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	token := readEndpointToken(t, ctx, lifecycle.EndpointResult{Endpoints: first.Endpoints[:1]})
	verifyAdvertisedURL(t, ctx, first)
	original, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	current, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.Endpoints[0].Addresses.Host.Port != second.Endpoints[0].Addresses.Host.Port {
		t.Fatal("no-change startup moved port")
	}
	for _, old := range original {
		found := false
		for _, now := range current {
			if now.ID == old.ID {
				found = true
			}
		}
		if !found {
			t.Fatal("no-change startup recreated a consumer or producer")
		}
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down}, io.Discard); err != nil {
		t.Fatal(err)
	}
	third, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if third.Endpoints[0].Addresses.Host.Port != first.Endpoints[0].Addresses.Host.Port || readEndpointToken(t, ctx, lifecycle.EndpointResult{Endpoints: third.Endpoints[:1]}) != token {
		t.Fatal("down/up lost port preference or volume data")
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(first.Endpoints[0].Addresses.Host.Port))))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	}()
	replacement, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Endpoints[0].Addresses.Host.Port == first.Endpoints[0].Addresses.Host.Port {
		t.Fatal("occupied preference reused")
	}
	verifyAdvertisedURL(t, ctx, replacement)
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal("unrelated listener was lost", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := store.Read()
	if err != nil || len(registry.Ports) != 1 || registry.Ports[0].HostPort != replacement.Endpoints[0].Addresses.Host.Port {
		t.Fatal("verified mapping not remembered", err)
	}
}

func verifyAdvertisedURL(t *testing.T, ctx context.Context, result lifecycle.EndpointResult) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, result.Endpoints[1].Addresses.Host.URL+"/url", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != result.Endpoints[0].Addresses.Host.URL {
		t.Fatalf("advertised stale address: %q %v", body, err)
	}
}

// A second, explicitly owned fixture takes the selected port after Compose
// creates the producer but before it starts. No mocks or global cleanup.
type stealAllocatedPort struct {
	ctx          context.Context
	target       *compose.Target
	registration workspace.Registration
	contender    workspace.Registration
	limit        int
	occupied     []uint16
	output       bytes.Buffer
}

func (w *stealAllocatedPort) Write(data []byte) (int, error) {
	if _, err := w.output.Write(data); err != nil {
		return 0, err
	}
	name := w.registration.ProjectName() + "-api-1 "
	if len(w.occupied) >= w.limit || !strings.Contains(string(data), name+"Created") && !strings.Contains(string(data), name+"Recreated") {
		return len(data), nil
	}
	inspected, err := w.target.Client.ContainerInspect(w.ctx, w.registration.ProjectName()+"-api-1", client.ContainerInspectOptions{})
	if err != nil {
		return 0, err
	}
	if inspected.Container.Config.Labels[compose.WorkspaceLabel] != w.registration.ID {
		return 0, fmt.Errorf("unexpected producer ownership")
	}
	port, err := network.ParsePort("8000/tcp")
	if err != nil {
		return 0, err
	}
	published := inspected.Container.HostConfig.PortBindings[port][0].HostPort
	hostPort, err := strconv.ParseUint(published, 10, 16)
	if err != nil {
		return 0, err
	}
	created, err := w.target.Client.ContainerCreate(w.ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: "busybox:1.37", Cmd: []string{"sleep", "300"}, ExposedPorts: network.PortSet{port: {}},
		Labels: map[string]string{api.ProjectLabel: w.contender.ProjectName(), api.ServiceLabel: "contender", api.ConfigHashLabel: "fixture", api.OneoffLabel: "False", api.ContainerNumberLabel: strconv.Itoa(len(w.occupied) + 1), compose.WorkspaceLabel: w.contender.ID},
	}, HostConfig: &container.HostConfig{PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: published}}}}})
	if err != nil {
		return 0, err
	}
	if _, err := w.target.Client.ContainerStart(w.ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return 0, err
	}
	w.occupied = append(w.occupied, uint16(hostPort))
	return len(data), nil
}

func TestAllocatedPortBindRaceAndRetryExhaustion(t *testing.T) {
	for _, conflicts := range []int{1, 3} {
		t.Run(strconv.Itoa(conflicts), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			main, store, registration, target := upWorkspace(t, ctx)
			_, _, contender, _ := upWorkspace(t, ctx)
			writeSelfBinding(t, main)
			output := &stealAllocatedPort{ctx: ctx, target: target, registration: registration, contender: contender, limit: conflicts}
			result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, output)
			if len(output.occupied) != conflicts {
				t.Fatalf("race not exercised: %v %v\n%s", output.occupied, err, &output.output)
			}
			if conflicts == 3 {
				if err == nil || result.SchemaVersion != 0 || !strings.Contains(err.Error(), "allocate") {
					t.Fatalf("exhaustion reported success: %v\n%s", err, &output.output)
				}
			} else {
				if err != nil {
					t.Fatalf("retry: %v\n%s", err, &output.output)
				}
				readEndpointToken(t, ctx, result)
				if result.Endpoints[0].Addresses.Host.Port == output.occupied[0] || !strings.Contains(output.output.String(), "retrying") {
					t.Fatal("conflict did not replan")
				}
				containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
				if err != nil {
					t.Fatal(err)
				}
				inspected, err := target.Client.ContainerInspect(ctx, containers[0].ID, client.ContainerInspectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(inspected.Container.Config.Env, "SELF_URL="+result.Endpoints[0].Addresses.Host.URL) {
					t.Fatal("consumer retained stale address")
				}
			}
			for _, port := range output.occupied {
				lease, acquired, err := store.TryClaimPort(ctx, port)
				if err != nil || !acquired {
					t.Fatal("attempt leaked a claim", err)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
			}
			untouched, err := target.Containers(ctx, contender, api.PsOptions{All: true})
			if err != nil || len(untouched) != conflicts {
				t.Fatal("contenders lost", err)
			}
			for _, ctr := range untouched {
				if ctr.State != container.StateRunning {
					t.Fatal("unrelated listener stopped")
				}
			}
			if conflicts == 3 {
				if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
					t.Fatal("exhaustion not recoverable", err)
				}
			}
		})
	}
}

func TestConcurrentAllocatedStartupAcrossProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	writeSelfBinding(t, main)
	_, other := repository(t)
	for name, content := range map[string]string{"compose.yaml": upCompose, "trocker.yaml": selfBindingManifest} {
		if err := os.WriteFile(filepath.Join(other, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	second, err := lifecycle.Init(ctx, other, store, func(lifecycle.InitProposal) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanupProject(t, cleanup, target, second)
	})
	held := allocatedProcess(t, ctx, main, store.Directory, true)
	stdout, err := held.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := held.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	}()
	held.Stderr = os.Stderr
	if err := held.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if held.ProcessState == nil {
			if err := held.Process.Kill(); err != nil {
				t.Error(err)
			}
			if err := held.Wait(); err == nil {
				t.Error("killed helper succeeded")
			}
		}
	})
	decoder := json.NewDecoder(stdout)
	var port uint16
	if err := decoder.Decode(&port); err != nil || port == 0 {
		t.Fatal("pending-port handshake", err)
	}
	// A stale preference may equal another operation's not-yet-bound candidate.
	// The held operation must block this port, not the whole registry or daemon.
	if err := store.RememberPorts(ctx, []state.PortPreference{{WorkspaceID: second.ID, TargetID: target.ID, Service: "api", ContainerPort: 8000, HostPort: port}}); err != nil {
		t.Fatal(err)
	}
	running := allocatedProcess(t, ctx, other, store.Directory, false)
	data, err := running.Output()
	if err != nil {
		t.Fatalf("independent startup blocked or failed: %v", err)
	}
	var otherResult lifecycle.EndpointResult
	if err := json.NewDecoder(strings.NewReader(string(data))).Decode(&otherResult); err != nil {
		t.Fatalf("helper output: %q %v", data, err)
	}
	if otherResult.Endpoints[0].Addresses.Host.Port == port {
		t.Fatal("processes selected the same port")
	}
	readEndpointToken(t, ctx, otherResult)
	if _, err := fmt.Fprintln(stdin, "continue"); err != nil {
		t.Fatal(err)
	}
	var firstResult lifecycle.EndpointResult
	if err := decoder.Decode(&firstResult); err != nil {
		t.Fatal(err)
	}
	if err := held.Wait(); err != nil {
		t.Fatal(err)
	}
	if firstResult.Endpoints[0].Addresses.Host.Port != port || firstResult.WorkspaceID != registration.ID {
		t.Fatal("pending allocation was stolen")
	}
	readEndpointToken(t, ctx, firstResult)
	registry, err := store.Read()
	if err != nil || len(registry.Ports) != 2 {
		t.Fatal("concurrent preferences lost", err)
	}
}

func allocatedProcess(t *testing.T, ctx context.Context, main, directory string, hold bool) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestAllocatedStartupProcessHelper$") //nolint:gosec // Runs this test binary with a fixed helper entry point.
	command.Env = append(os.Environ(), "TROCKER_TEST_ALLOCATION_MAIN="+main, "TROCKER_TEST_ALLOCATION_STATE="+directory, "TROCKER_TEST_ALLOCATION_HOLD="+strconv.FormatBool(hold))
	return command
}

func TestAllocatedStartupProcessHelper(t *testing.T) {
	main := os.Getenv("TROCKER_TEST_ALLOCATION_MAIN")
	if main == "" {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	store := state.Store{Directory: os.Getenv("TROCKER_TEST_ALLOCATION_STATE")}
	output := io.Discard
	if os.Getenv("TROCKER_TEST_ALLOCATION_HOLD") == "true" {
		preview, err := lifecycle.Plan(ctx, main, store, nil)
		if err != nil {
			t.Fatal(err)
		}
		target, err := compose.ResolveTarget(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := target.Close(); err != nil {
				t.Error(err)
			}
		}()
		output = &holdAllocatedPort{ctx: ctx, target: target, registration: preview.Registration}
	}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
}

type holdAllocatedPort struct {
	ctx          context.Context
	target       *compose.Target
	registration workspace.Registration
	held         bool
}

func (w *holdAllocatedPort) Write(data []byte) (int, error) {
	if w.held || !strings.Contains(string(data), w.registration.ProjectName()+"-api-1 Created") {
		return len(data), nil
	}
	w.held = true
	inspected, err := w.target.Client.ContainerInspect(w.ctx, w.registration.ProjectName()+"-api-1", client.ContainerInspectOptions{})
	if err != nil {
		return 0, err
	}
	if inspected.Container.Config.Labels[compose.WorkspaceLabel] != w.registration.ID {
		return 0, fmt.Errorf("unexpected fixture ownership")
	}
	port, err := network.ParsePort("8000/tcp")
	if err != nil {
		return 0, err
	}
	published, err := strconv.ParseUint(inspected.Container.HostConfig.PortBindings[port][0].HostPort, 10, 16)
	if err != nil {
		return 0, err
	}
	if err := json.NewEncoder(os.Stdout).Encode(published); err != nil {
		return 0, err
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return 0, err
	}
	return len(data), nil
}

func stableWorkspace(t *testing.T, ctx context.Context) (string, state.Store, workspace.Registration, *compose.Target, uint16) {
	t.Helper()
	composeContent := strings.Replace(upCompose, "expose: ['8000']", "ports: ['8000']", 1)
	manifest := strings.Replace(upManifest, "endpoints:", "stable_ports: true\nendpoints:", 1)
	main, store, registration, target := workspaceFixture(t, ctx, composeContent, manifest)
	assigned, err := lifecycle.Ports(ctx, main, store)
	if err != nil {
		t.Fatal(err)
	}
	// Compose declares no host port, so the assignment must come from the stable range, not the OS.
	if len(assigned.Ports) != 1 || assigned.Ports[0].Service != "api" || assigned.Ports[0].ContainerPort != 8000 || !config.DefaultStablePortRange.Contains(assigned.Ports[0].HostPort) {
		t.Fatalf("init assignments = %+v", assigned)
	}
	return main, store, registration, target, assigned.Ports[0].HostPort
}

func TestStablePortsAssignBeforeStartupAndPersist(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, _, hostPort := stableWorkspace(t, ctx)

	preview, err := lifecycle.Plan(ctx, main, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.StablePorts || preview.Publications != 1 || preview.AssignedPorts != 1 {
		t.Fatalf("preview counts = %+v", preview)
	}
	if got := preview.Project.Services["api"].Ports[0].Published; got != strconv.Itoa(int(hostPort)) {
		t.Fatalf("preview publication = %q, want %d", got, hostPort)
	}

	first, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Endpoints) != 1 || first.Endpoints[0].Addresses.Host == nil || first.Endpoints[0].Addresses.Host.Port != hostPort {
		t.Fatalf("published endpoint = %+v", first.Endpoints)
	}
	readEndpointToken(t, ctx, first)

	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Down}, io.Discard); err != nil {
		t.Fatal(err)
	}
	afterDown, err := lifecycle.Ports(ctx, main, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterDown.Ports) != 1 || afterDown.Ports[0].HostPort != hostPort {
		t.Fatalf("assignment did not survive down: %+v", afterDown.Ports)
	}
	second, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkspaceID != registration.ID || second.Endpoints[0].Addresses.Host == nil || second.Endpoints[0].Addresses.Host.Port != hostPort {
		t.Fatalf("restart did not reuse stable port: %+v", second.Endpoints)
	}
}

func TestStablePortConflictDoesNotFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, target, hostPort := stableWorkspace(t, ctx)

	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(hostPort))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err == nil || !strings.Contains(err.Error(), "stable host port") {
		t.Fatalf("stable conflict = %v", err)
	}
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(containers) != 0 {
		t.Fatalf("conflicting startup created containers: %v, %v", containers, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.Endpoints[0].Addresses.Host == nil || result.Endpoints[0].Addresses.Host.Port != hostPort {
		t.Fatal("recovery changed the stable assignment")
	}
	readEndpointToken(t, ctx, result)
}
