package lifecycle_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
)

func TestEndpointObservations(t *testing.T) {
	t.Parallel()
	definition := map[string]config.Endpoint{"api": {Service: "backend", Port: 8000, Scheme: "http"}}
	mapping := api.PortPublisher{URL: "0.0.0.0", TargetPort: 8000, PublishedPort: 24173, Protocol: "tcp"}
	for _, test := range []struct {
		name       string
		containers []api.ContainerSummary
		state, url string
		fail       bool
	}{
		{name: "missing", state: "missing"},
		{name: "running without publication", containers: []api.ContainerSummary{{Service: "backend", State: container.StateRunning}}, state: "missing"},
		{name: "stopped stale mapping", containers: []api.ContainerSummary{{Service: "backend", State: container.StateExited, Publishers: api.PortPublishers{mapping}}}, state: "stopped"},
		{name: "unhealthy but published", containers: []api.ContainerSummary{{Service: "backend", State: container.StateRunning, Health: container.Unhealthy, Publishers: api.PortPublishers{mapping}}}, state: "published", url: "http://127.0.0.1:24173"},
		{name: "multiple mappings", containers: []api.ContainerSummary{{Service: "backend", State: container.StateRunning, Publishers: api.PortPublishers{mapping, mapping}}}, fail: true},
		{name: "replicas", containers: []api.ContainerSummary{{Service: "backend"}, {Service: "backend"}}, fail: true},
		{name: "one-off ignored", containers: []api.ContainerSummary{{Service: "backend", State: container.StateRunning, Labels: map[string]string{api.OneoffLabel: "True"}, Publishers: api.PortPublishers{mapping}}}, state: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoints, err := lifecycle.ResolveEndpoints(definition, test.containers)
			if test.fail {
				if err == nil || endpoints != nil {
					t.Fatal("ambiguous mapping accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			endpoint := endpoints[0]
			if endpoint.State != test.state {
				t.Fatalf("state=%s", endpoint.State)
			}
			if test.url == "" {
				if endpoint.Addresses.Host != nil {
					t.Fatal("unavailable endpoint exposed stale address")
				}
			} else if endpoint.Addresses.Host == nil || endpoint.Addresses.Host.URL != test.url {
				t.Fatalf("host=%+v", endpoint.Addresses.Host)
			}
			if endpoint.Addresses.Container.URL != "http://backend:8000" {
				t.Fatal("container address is not configuration-derived")
			}
		})
	}
}

func TestEndpointJSONAndIPv6(t *testing.T) {
	t.Parallel()
	endpoints, err := lifecycle.ResolveEndpoints(map[string]config.Endpoint{
		"web": {Service: "web", Port: 80, Scheme: "https"}, "db": {Service: "db", Port: 5432, Scheme: "tcp"},
	}, []api.ContainerSummary{{Service: "web", State: container.StateRunning, Publishers: api.PortPublishers{{URL: "::", TargetPort: 80, PublishedPort: 24443, Protocol: "tcp"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if endpoints[0].Name != "db" || endpoints[1].Addresses.Host.URL != "https://[::1]:24443" {
		t.Fatalf("endpoints=%+v", endpoints)
	}
	data, err := json.Marshal(endpoints[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"url"`) || !strings.Contains(string(data), `"host":null`) {
		t.Fatalf("invalid unavailable TCP representation: %s", data)
	}
	for _, endpoint := range endpoints {
		data, err := json.Marshal(endpoint.Addresses)
		if err != nil {
			t.Fatal(err)
		}
		var addresses map[string]json.RawMessage
		if err := json.Unmarshal(data, &addresses); err != nil {
			t.Fatal(err)
		}
		if len(addresses) != 2 || addresses["container"] == nil || addresses["host"] == nil {
			t.Fatalf("unexpected endpoint representations: %s", data)
		}
	}
	empty, err := lifecycle.ResolveEndpoints(nil, nil)
	if err != nil || empty == nil {
		t.Fatal("empty endpoints must encode as an array")
	}
}
