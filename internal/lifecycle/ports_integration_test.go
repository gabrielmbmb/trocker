//go:build integration

package lifecycle_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
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
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
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
	preview, err := lifecycle.Plan(ctx, main, store)
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
	original, err := target.Containers(ctx, registration)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Up(ctx, main, store, options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	current, err := target.Containers(ctx, registration)
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
				containers, err := target.Containers(ctx, registration)
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
			untouched, err := target.Containers(ctx, contender)
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
