package plan_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func fixture(t *testing.T, content string) (*config.Manifest, string) {
	t.Helper()
	path := fixtureWith(t, content, "version: 1\ncompose: [compose.yaml]\n")
	manifest, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, path
}

func fixtureWith(t *testing.T, composeContent, manifestContent string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte(composeContent), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "trocker.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

func TestPlanIsIsolatedAndDoesNotMutateInputs(t *testing.T) {
	_, path := fixture(t, `services:
  api:
    image: busybox:1.37
    container_name: fixed
    ports: ["8000:8000"]
    environment: {SECRET: do-not-print}
    volumes: ["data:/data"]
  web:
    build: .
    image: shared-build-tag
    depends_on: [api]
volumes:
  data:
`)
	manifest, source, err := compose.LoadForPlan(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Endpoints = map[string]config.Endpoint{"api": {Service: "api", Port: 8000, Scheme: "http"}}
	manifest.Bindings = map[string]config.Bindings{"web": {Environment: map[string]string{"API_URL": "{{ endpoints.api.container.url }}", "ID": "{{ workspace.id }}"}}}
	before, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	project, err := plan.Build(manifest, source, registration, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != registration.ProjectName() || project.Services["api"].ContainerName != "" {
		t.Fatal("workspace names were not applied")
	}
	if *project.Services["web"].Environment["API_URL"] != "http://api:8000" || *project.Services["api"].Environment["TROCKER_WORKSPACE_ID"] != registration.ID {
		t.Fatal("bindings or identity missing")
	}
	if project.Services["api"].Ports[0].Published != "0" || project.Services["api"].Ports[0].HostIP != "127.0.0.1" || len(project.Services["api"].Ports) != 1 {
		t.Fatal("publications were appended or retained")
	}
	if !strings.HasPrefix(project.Services["web"].Image, project.Name) || project.Services["api"].Image != "busybox:1.37" {
		t.Fatal("build and supplied image policies confused")
	}
	if project.Volumes["data"].Name != project.Name+"_data" || project.Networks["default"].Labels[plan.WorkspaceLabel] != registration.ID {
		t.Fatal("resources not isolated")
	}
	after, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("planner mutated its input")
	}
	second, err := plan.Build(manifest, source, registration, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("planning is not deterministic")
	}
	other, err := plan.Build(manifest, source, workspace.Registration{ID: "17aa318c-630e-4e56-bca4-452188f698d3"}, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if other.Volumes["data"].Name == project.Volumes["data"].Name || other.Services["web"].Image == project.Services["web"].Image {
		t.Fatal("workspaces share managed names")
	}
}

func TestWritableMountsRequireDeclaredPaths(t *testing.T) {
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	withPaths := func(paths string) string {
		return "version: 1\ncompose: [compose.yaml]\nread_write_paths: [" + paths + "]\n"
	}
	const undeclared = "version: 1\ncompose: [compose.yaml]\n"
	for _, test := range []struct {
		name     string
		compose  string
		manifest string
		readOnly bool
		problem  string
	}{
		{
			name:     "declared path",
			compose:  "services: {api: {image: busybox, volumes: ['./src:/app']}}",
			manifest: withPaths("./src"),
		},
		{
			name:     "nested under declared path",
			compose:  "services: {api: {image: busybox, volumes: ['./src/pkg:/app']}}",
			manifest: withPaths("./src"),
		},
		{
			name:     "sibling prefix",
			compose:  "services: {api: {image: busybox, volumes: ['./src-cache:/cache']}}",
			manifest: withPaths("./src"),
			problem:  "not declared in read_write_paths",
		},
		{
			name:     "undeclared path",
			compose:  "services: {api: {image: busybox, volumes: ['./src:/app']}}",
			manifest: undeclared,
			problem:  "not declared in read_write_paths",
		},
		{
			name:     "outside checkout",
			compose:  "services: {api: {image: busybox, volumes: ['../outside:/app']}}",
			manifest: withPaths("."),
			problem:  "outside the checkout",
		},
		{
			name:     "read-only needs no declaration",
			compose:  "services: {api: {image: busybox, volumes: ['./src:/app:ro']}}",
			manifest: undeclared,
			readOnly: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest, source, err := compose.LoadForPlan(t.Context(), fixtureWith(t, test.compose, test.manifest))
			if err != nil {
				t.Fatal(err)
			}
			project, err := plan.Build(manifest, source, registration, plan.Options{})
			if test.problem != "" {
				var help interface{ Help() string }
				if err == nil || !strings.Contains(err.Error(), test.problem) {
					t.Fatalf("error = %v, want %q", err, test.problem)
				}
				if !errors.As(err, &help) || help.Help() == "" {
					t.Fatalf("error has no recovery guidance: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			volumes := project.Services["api"].Volumes
			if len(volumes) != 1 || volumes[0].Type != types.VolumeTypeBind || volumes[0].ReadOnly != test.readOnly {
				t.Fatalf("bind mount was not preserved: %#v", volumes)
			}
		})
	}
}

func TestUnsupportedConfigurationsFailBeforeMutation(t *testing.T) {
	for name, content := range map[string]string{
		"dependency cycle": "services: {api: {image: busybox, depends_on: [web]}, web: {image: busybox, depends_on: [api]}}",
		"explicit network": "services: {api: {image: busybox}}\nnetworks: {default: {name: shared}}",
		"explicit volume":  "services: {api: {image: busybox, volumes: ['data:/data']}}\nvolumes: {data: {name: shared}}",
		"external":         "services: {api: {image: busybox}}\nnetworks: {default: {external: true}}",
		"host network":     "services: {api: {image: busybox, network_mode: host}}",
		"writable bind":    "services: {api: {image: busybox, volumes: ['./data:/data']}}",
		"UDP":              "services: {api: {image: busybox, ports: ['5353:53/udp']}}",
		"privileged":       "services: {api: {image: busybox, privileged: true}}",
		"identity":         "services: {api: {image: busybox, environment: {TROCKER_WORKSPACE_ID: do-not-print}}}",
	} {
		t.Run(name, func(t *testing.T) {
			_, path := fixture(t, content)
			manifest, source, err := compose.LoadForPlan(t.Context(), path)
			if err == nil {
				_, err = plan.Build(manifest, source, workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}, plan.Options{})
			}
			if err == nil || strings.Contains(err.Error(), "do-not-print") {
				t.Fatalf("unexpected diagnostic: %v", err)
			}
		})
	}
}

func TestHostBindingsRequireAllocation(t *testing.T) {
	_, path := fixture(t, "services: {api: {image: busybox, expose: ['8000']}}")
	manifest, source, err := compose.LoadForPlan(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Endpoints = map[string]config.Endpoint{"api": {Service: "api", Port: 8000, Scheme: "http"}}
	manifest.Bindings = map[string]config.Bindings{"api": {Environment: map[string]string{"URL": "secret-{{ endpoints.api.host.url }}"}}}
	if _, err := plan.Build(manifest, source, workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}, plan.Options{}); err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("unsupported startup binding: %v", err)
	}
}
