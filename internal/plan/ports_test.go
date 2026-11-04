package plan_test

import (
	"reflect"
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
