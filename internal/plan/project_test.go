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

func TestDockerSocketAccessRequiresServiceOptIn(t *testing.T) {
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	withServices := func(services string) string {
		return "version: 1\ncompose: [compose.yaml]\ndocker_socket_services: [" + services + "]\n"
	}
	const undeclared = "version: 1\ncompose: [compose.yaml]\n"
	for _, test := range []struct {
		name     string
		compose  string
		manifest string
		useAPI   bool
		problem  string
	}{
		{
			name:     "declared socket",
			compose:  "services: {api: {image: busybox, volumes: ['/var/run/docker.sock:/var/run/docker.sock']}}",
			manifest: withServices("api"),
		},
		{
			name:     "declared socket outside the conventional path",
			compose:  "services: {api: {image: busybox, volumes: ['/run/user/1000/docker.sock:/var/run/docker.sock']}}",
			manifest: withServices("api"),
		},
		{
			name:     "declared parent directory",
			compose:  "services: {api: {image: busybox, volumes: ['/var/run:/var/run:ro']}}",
			manifest: withServices("api"),
		},
		{
			name:     "declared use_api_socket",
			compose:  "services: {api: {image: busybox, use_api_socket: true}}",
			manifest: withServices("api"),
			useAPI:   true,
		},
		{
			name:     "undeclared socket",
			compose:  "services: {api: {image: busybox, volumes: ['/var/run/docker.sock:/var/run/docker.sock']}}",
			manifest: undeclared,
			problem:  "not declared in docker_socket_services",
		},
		{
			name:     "undeclared read-only socket",
			compose:  "services: {api: {image: busybox, volumes: ['/var/run/docker.sock:/var/run/docker.sock:ro']}}",
			manifest: undeclared,
			problem:  "not declared in docker_socket_services",
		},
		{
			name:     "undeclared parent directory",
			compose:  "services: {api: {image: busybox, volumes: ['/var/run:/var/run:ro']}}",
			manifest: undeclared,
			problem:  "not declared in docker_socket_services",
		},
		{
			name:     "undeclared use_api_socket",
			compose:  "services: {api: {image: busybox, use_api_socket: true}}",
			manifest: undeclared,
			problem:  "use_api_socket is not declared in docker_socket_services",
		},
		{
			name:     "other service declared",
			compose:  "services: {api: {image: busybox, volumes: ['/var/run/docker.sock:/var/run/docker.sock']}, web: {image: busybox}}",
			manifest: withServices("web"),
			problem:  "not declared in docker_socket_services",
		},
		{
			name:     "socket opt-in is not a writable path policy",
			compose:  "services: {api: {image: busybox, volumes: ['./data:/data', '/var/run/docker.sock:/var/run/docker.sock']}}",
			manifest: withServices("api"),
			problem:  "not declared in read_write_paths",
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
			service := project.Services["api"]
			if test.useAPI {
				if !service.UseAPISocket {
					t.Fatal("use_api_socket was not preserved")
				}
			} else if len(service.Volumes) == 0 || !plan.ExposesDockerSocket(service.Volumes[0].Source) {
				t.Fatalf("Docker socket access was not preserved: %#v", service.Volumes)
			}
		})
	}
}

func TestUnsupportedConfigurationsFailBeforeMutation(t *testing.T) {
	for name, content := range map[string]string{
		"dependency cycle":    "services: {api: {image: busybox, depends_on: [web]}, web: {image: busybox, depends_on: [api]}}",
		"explicit network":    "services: {api: {image: busybox}}\nnetworks: {default: {name: shared}}",
		"explicit volume":     "services: {api: {image: busybox, volumes: ['data:/data']}}\nvolumes: {data: {name: shared}}",
		"external":            "services: {api: {image: busybox}}\nnetworks: {default: {external: true}}",
		"host network":        "services: {api: {image: busybox, network_mode: host}}",
		"writable bind":       "services: {api: {image: busybox, volumes: ['./data:/data']}}",
		"UDP":                 "services: {api: {image: busybox, ports: ['5353:53/udp']}}",
		"privileged":          "services: {api: {image: busybox, privileged: true}}",
		"UTS namespace":       "services: {api: {image: busybox, uts: host}}",
		"cgroup namespace":    "services: {api: {image: busybox, cgroup: host}}",
		"device cgroup rules": "services: {api: {image: busybox, device_cgroup_rules: ['c 1:3 mr']}}",
		"identity":            "services: {api: {image: busybox, environment: {TROCKER_WORKSPACE_ID: do-not-print}}}",
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

func TestExposesDockerSocket(t *testing.T) {
	for _, test := range []struct {
		source  string
		exposes bool
	}{
		{source: "/var/run/docker.sock", exposes: true},
		{source: "/run/docker.sock", exposes: true},
		{source: "/run/user/1000/docker.sock", exposes: true},
		{source: "/home/dev/.docker/run/docker.sock", exposes: true},
		{source: "/var/run", exposes: true},
		{source: "/var", exposes: true},
		{source: "/", exposes: true},
		{source: "/checkout/src"},
		{source: "/var/log"},
		{source: "/var/run-docker"},
		{source: "/var/run/docker.sock.bak"},
		{source: "/run/secrets"},
	} {
		t.Run(test.source, func(t *testing.T) {
			if got := plan.ExposesDockerSocket(test.source); got != test.exposes {
				t.Fatalf("ExposesDockerSocket(%q) = %v, want %v", test.source, got, test.exposes)
			}
		})
	}
}

func TestHostAccessRequiresServiceGrant(t *testing.T) {
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	withGrant := func(fields string) string {
		return "version: 1\ncompose: [compose.yaml]\nhost_access: {api: {" + fields + "}}\n"
	}
	const undeclared = "version: 1\ncompose: [compose.yaml]\n"
	for _, test := range []struct {
		name     string
		compose  string
		manifest string
		granted  bool
		problem  string
	}{
		{
			name:     "declared pid",
			compose:  "services: {api: {image: busybox, pid: host}}",
			manifest: withGrant("pid: true"),
			granted:  true,
		},
		{
			name:     "undeclared pid",
			compose:  "services: {api: {image: busybox, pid: host}}",
			manifest: undeclared,
			problem:  "pid: host is not declared",
		},
		{
			name:     "unsupported pid",
			compose:  "services: {api: {image: busybox, pid: 'service:web'}, web: {image: busybox}}",
			manifest: withGrant("pid: true"),
			problem:  "only pid: host is supported",
		},
		{
			name:     "declared ipc",
			compose:  "services: {api: {image: busybox, ipc: host}}",
			manifest: withGrant("ipc: true"),
			granted:  true,
		},
		{
			name:     "private ipc needs no grant",
			compose:  "services: {api: {image: busybox, ipc: private}}",
			manifest: undeclared,
		},
		{
			name:     "undeclared ipc",
			compose:  "services: {api: {image: busybox, ipc: host}}",
			manifest: undeclared,
			problem:  "ipc: host is not declared",
		},
		{
			name:     "unsupported ipc",
			compose:  "services: {api: {image: busybox, ipc: shareable}}",
			manifest: withGrant("ipc: true"),
			problem:  "only ipc: host and ipc: private are supported",
		},
		{
			name:     "declared capability",
			compose:  "services: {api: {image: busybox, cap_add: [SYS_PTRACE]}}",
			manifest: withGrant("capabilities: [SYS_PTRACE]"),
			granted:  true,
		},
		{
			name:     "capability case and prefix",
			compose:  "services: {api: {image: busybox, cap_add: [cap_sys_ptrace]}}",
			manifest: withGrant("capabilities: [SYS_PTRACE]"),
			granted:  true,
		},
		{
			name:     "undeclared capability",
			compose:  "services: {api: {image: busybox, cap_add: [SYS_PTRACE]}}",
			manifest: undeclared,
			problem:  "cap_add SYS_PTRACE is not declared",
		},
		{
			name:     "one undeclared capability",
			compose:  "services: {api: {image: busybox, cap_add: [SYS_PTRACE, SYS_ADMIN]}}",
			manifest: withGrant("capabilities: [SYS_PTRACE]"),
			problem:  "cap_add SYS_ADMIN is not declared",
		},
		{
			name:     "capability ALL",
			compose:  "services: {api: {image: busybox, cap_add: [ALL]}}",
			manifest: withGrant("capabilities: [SYS_PTRACE]"),
			problem:  "cap_add ALL is not supported",
		},
		{
			name:     "declared device",
			compose:  "services: {api: {image: busybox, devices: ['/dev/kmsg:/dev/kmsg:rw']}}",
			manifest: withGrant("devices: [{source: /dev/kmsg, permissions: rw}]"),
			granted:  true,
		},
		{
			name:     "device default permissions",
			compose:  "services: {api: {image: busybox, devices: ['/dev/kmsg']}}",
			manifest: withGrant("devices: [{source: /dev/kmsg, permissions: rwm}]"),
			granted:  true,
		},
		{
			name:     "device permissions are exact",
			compose:  "services: {api: {image: busybox, devices: ['/dev/kmsg:/dev/kmsg:rwm']}}",
			manifest: withGrant("devices: [{source: /dev/kmsg, permissions: rw}]"),
			problem:  "device /dev/kmsg (rwm) is not declared",
		},
		{
			name:     "undeclared device",
			compose:  "services: {api: {image: busybox, devices: ['/dev/kmsg']}}",
			manifest: undeclared,
			problem:  "device /dev/kmsg (rwm) is not declared",
		},
		{
			name:     "other service granted",
			compose:  "services: {api: {image: busybox, pid: host}, web: {image: busybox}}",
			manifest: "version: 1\ncompose: [compose.yaml]\nhost_access: {web: {pid: true}}\n",
			problem:  "pid: host is not declared",
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
			service := project.Services["api"]
			if test.granted && service.Pid != "host" && service.Ipc != "host" && len(service.CapAdd) == 0 && len(service.Devices) == 0 {
				t.Fatal("host access was not preserved")
			}
		})
	}
}

func TestBuildContextsRequireDeclaredPaths(t *testing.T) {
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	withContexts := func(paths string) string {
		return "version: 1\ncompose: [compose.yaml]\nbuild_contexts: [" + paths + "]\n"
	}
	const undeclared = "version: 1\ncompose: [compose.yaml]\n"
	for _, test := range []struct {
		name     string
		compose  string
		manifest string
		problem  string
	}{
		{
			name:     "declared path",
			compose:  "services: {api: {image: busybox, build: {context: '.', additional_contexts: {shared: ./packages}}}}",
			manifest: withContexts("./packages"),
		},
		{
			name:     "nested under declared path",
			compose:  "services: {api: {image: busybox, build: {context: '.', additional_contexts: {shared: ./packages/sdk}}}}",
			manifest: withContexts("./packages"),
		},
		{
			name:     "no additional contexts needs no declaration",
			compose:  "services: {api: {image: busybox, build: {context: '.'}}}",
			manifest: undeclared,
		},
		{
			name:     "sibling prefix",
			compose:  "services: {api: {image: busybox, build: {context: '.', additional_contexts: {shared: ./packages-cache}}}}",
			manifest: withContexts("./packages"),
			problem:  "not declared in build_contexts",
		},
		{
			name:     "undeclared path",
			compose:  "services: {api: {image: busybox, build: {context: '.', additional_contexts: {shared: ./packages}}}}",
			manifest: undeclared,
			problem:  "not declared in build_contexts",
		},
		{
			name:     "outside checkout",
			compose:  "services: {api: {image: busybox, build: {context: '.', additional_contexts: {shared: ../outside}}}}",
			manifest: withContexts("."),
			problem:  "outside the checkout",
		},
		{
			name:     "service reference",
			compose:  "services: {api: {image: busybox, build: {context: '.', additional_contexts: {shared: 'service:web'}}}, web: {image: busybox, build: {context: '.'}}}",
			manifest: withContexts("."),
			problem:  "refers to another service",
		},
		{
			name:     "remote context",
			compose:  "services: {api: {image: busybox, build: {context: '.', additional_contexts: {shared: 'https://user:token@example.com/repo.git'}}}}",
			manifest: withContexts("."),
			problem:  "is not a local path",
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
				if strings.Contains(err.Error(), "token") {
					t.Fatalf("error exposed remote credentials: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			contexts := project.Services["api"].Build.AdditionalContexts
			if len(contexts) != len(source.Services["api"].Build.AdditionalContexts) {
				t.Fatalf("additional contexts were not preserved: %#v", contexts)
			}
		})
	}
}

func TestSharedWritePathsAllowExternalMountsWithoutCreatingThem(t *testing.T) {
	registration := workspace.Registration{ID: "e353936d-8f28-4b8e-a7a2-a629702886bd"}
	withPaths := func(paths string) string {
		return "version: 1\ncompose: [compose.yaml]\nshared_write_paths: [" + paths + "]\n"
	}
	const undeclared = "version: 1\ncompose: [compose.yaml]\n"
	for _, test := range []struct {
		name     string
		compose  string
		manifest string
		problem  string
		readOnly bool
	}{
		{
			name:     "declared external path",
			compose:  "services: {api: {image: busybox, volumes: ['/var/cache/shared:/cache']}}",
			manifest: withPaths("/var/cache/shared"),
		},
		{
			name:     "nested under declared external path",
			compose:  "services: {api: {image: busybox, volumes: ['/var/cache/shared/nested:/cache']}}",
			manifest: withPaths("/var/cache/shared"),
		},
		{
			name:     "long syntax",
			compose:  "services: {api: {image: busybox, volumes: [{type: bind, source: /var/cache/shared, target: /cache}]}}",
			manifest: withPaths("/var/cache/shared"),
		},
		{
			name:     "sibling prefix",
			compose:  "services: {api: {image: busybox, volumes: ['/var/cache/shared-extra:/cache']}}",
			manifest: withPaths("/var/cache/shared"),
			problem:  "not declared in shared_write_paths",
		},
		{
			name:     "undeclared external path",
			compose:  "services: {api: {image: busybox, volumes: ['/var/cache/shared:/cache']}}",
			manifest: undeclared,
			problem:  "outside the checkout and not declared in shared_write_paths",
		},
		{
			name:     "read-only external path needs no declaration",
			compose:  "services: {api: {image: busybox, volumes: ['/var/cache/shared:/cache:ro']}}",
			manifest: undeclared,
			readOnly: true,
		},
		{
			name:     "shared path does not authorize a checkout path",
			compose:  "services: {api: {image: busybox, volumes: ['./data:/data']}}",
			manifest: withPaths("/var/cache/shared"),
			problem:  "not declared in read_write_paths",
		},
		{
			name:     "shared path does not authorize the docker socket",
			compose:  "services: {api: {image: busybox, volumes: ['/var/run/docker.sock:/var/run/docker.sock']}}",
			manifest: withPaths("/var/run"),
			problem:  "not declared in docker_socket_services",
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
			mount := project.Services["api"].Volumes[0]
			if test.readOnly {
				if !mount.ReadOnly {
					t.Fatalf("read-only mount was changed: %#v", mount)
				}
				return
			}
			if mount.Bind == nil || bool(mount.Bind.CreateHostPath) {
				t.Fatalf("shared bind mount may create a missing host path: %#v", mount.Bind)
			}
		})
	}
}
