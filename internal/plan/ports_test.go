package plan_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestAllocatedBindingsAndAliases(t *testing.T) {
	_, path := fixture(t, "services:\n  api: {image: busybox, expose: ['8000','9000']}\n  web: {image: busybox, depends_on: [api]}\n")
	manifest, source, err := compose.LoadForPlan(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Endpoints = map[string]config.Endpoint{
		"api":      {Service: "api", Port: 8000, Scheme: "http"},
		"alias":    {Service: "api", Port: 8000, Scheme: "http"},
		"unneeded": {Service: "api", Port: 9000, Scheme: "http"},
	}
	manifest.Bindings = map[string]config.Bindings{
		"web": {Environment: map[string]string{"URL": "{{ endpoints.api.host.url }}", "PORT": "{{ endpoints.alias.host.port }}", "HOST": "{{ endpoints.unneeded.host.host }}", "INTERNAL": "{{ endpoints.api.container.url }}"}},
		"api": {Environment: map[string]string{"SELF": "{{ endpoints.alias.host.url }}"}},
	}
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	preview, err := plan.Build(manifest, source, registration, plan.Options{Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	required, err := plan.RequiredPorts(manifest, preview)
	expected := []plan.PortKey{{Service: "api", Port: 8000}}
	if err != nil || !reflect.DeepEqual(required, expected) {
		t.Fatalf("requirements: %v %v", required, err)
	}
	if preview.Services["api"].Ports[0].Published != "0" {
		t.Fatal("preview assigned a port")
	}
	project, err := plan.Build(manifest, source, registration, plan.Options{Ports: map[plan.PortKey]uint16{expected[0]: 24001}})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"URL": "http://127.0.0.1:24001", "PORT": "24001", "HOST": "127.0.0.1", "INTERNAL": "http://api:8000"} {
		if got := project.Services["web"].Environment[name]; got == nil || *got != value {
			t.Fatalf("%s = %v", name, got)
		}
	}
	if *project.Services["api"].Environment["SELF"] != "http://127.0.0.1:24001" {
		t.Fatal("self-advertised URL not allocated")
	}
	ports := project.Services["api"].Ports
	if len(ports) != 2 || ports[0].Published != "24001" || ports[1].Published != "0" {
		t.Fatal("aliases or unneeded ports changed publications", ports)
	}
	if len(source.Services["api"].Ports) != 0 || source.Services["web"].Environment["URL"] != nil {
		t.Fatal("mutated source")
	}
	selected, err := plan.Build(manifest, source, registration, plan.Options{Services: []string{"api"}, Preview: true})
	if err != nil || len(selected.Services) != 1 {
		t.Fatal("selection changed", err)
	}
}

// A stable host port is known before its service starts, so a consumer may
// name it while the run leaves that service out. Without stable ports the port
// is chosen at startup, and a container reference needs the service itself.
func TestBindingsToServicesLeftOutOfTheRun(t *testing.T) {
	_, path := fixture(t, "services:\n  api: {image: busybox, expose: ['8000']}\n  web: {image: busybox}\n")
	api := plan.PortKey{Service: "api", Port: 8000}
	for _, test := range []struct {
		name      string
		stable    bool
		reference string
		ports     map[plan.PortKey]uint16
		want      string
		failure   string
	}{
		{name: "stable host url", stable: true, reference: "host.url", ports: map[plan.PortKey]uint16{api: 24001}, want: "http://127.0.0.1:24001"},
		{name: "stable port not assigned yet", stable: true, reference: "host.url", failure: "no stable host port yet"},
		{name: "ports chosen at startup", reference: "host.url", ports: map[plan.PortKey]uint16{api: 24001}, failure: "not selected"},
		{name: "container address", stable: true, reference: "container.url", ports: map[plan.PortKey]uint16{api: 24001}, failure: "not selected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest, source, err := compose.LoadForPlan(t.Context(), path, nil)
			if err != nil {
				t.Fatal(err)
			}
			manifest.StablePorts = test.stable
			manifest.Endpoints = map[string]config.Endpoint{"api": {Service: "api", Port: 8000, Scheme: "http"}}
			manifest.Bindings = map[string]config.Bindings{"web": {Environment: map[string]string{"URL": "{{ endpoints.api." + test.reference + " }}"}}}
			project, err := plan.Build(manifest, source, workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}, plan.Options{Services: []string{"web"}, Ports: test.ports})
			if test.failure != "" {
				if err == nil || !strings.Contains(err.Error(), test.failure) {
					t.Fatalf("error = %v, want %q", err, test.failure)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, included := project.Services["api"]; included {
				t.Fatal("binding added its endpoint service to the run")
			}
			if got := project.Services["web"].Environment["URL"]; got == nil || *got != test.want {
				t.Fatalf("URL = %v, want %s", got, test.want)
			}
		})
	}
}

func TestPublicationsIncludeComposeAndEndpointPorts(t *testing.T) {
	_, path := fixture(t, `services:
  api:
    image: busybox:1.37
    ports: ["18000:8000"]
    expose: ['9000']
`)
	manifest, source, err := compose.LoadForPlan(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Endpoints = map[string]config.Endpoint{"extra": {Service: "api", Port: 9000, Scheme: "http"}}
	publications := plan.Publications(manifest, source)
	expected := []plan.PortKey{{Service: "api", Port: 8000}, {Service: "api", Port: 9000}}
	if !reflect.DeepEqual(publications, expected) {
		t.Fatalf("publications = %v, want %v", publications, expected)
	}
	if port, ok := plan.ComposeHostPort(source, expected[0]); !ok || port != 18000 {
		t.Fatalf("Compose host port = %d, %v", port, ok)
	}
	if _, ok := plan.ComposeHostPort(source, expected[1]); ok {
		t.Fatal("exposed-only port has a concrete Compose publication")
	}
}
