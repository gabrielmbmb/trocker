package compose_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/generated"
)

// These exercise the real SDK loader, not a Docker daemon.
func TestContractFixtures(t *testing.T) {
	cleanEnvironment(t)
	for _, test := range []struct{ file, field string }{
		{"minimal.yaml", ""},
		{"bindings.yaml", ""},
		{"native-files.yaml", ""},
		{"container-files.yaml", ""},
		{"host-before-start.yaml", ""},
		{"unsupported-version.yaml", "version"},
		{"duplicate-key.yaml", "manifest"},
		{"unknown-field.yaml", "manifest"},
		{"invalid-reference.yaml", "bindings.web.environment.API_URL"},
		{"undeclared-port.yaml", "endpoints.api.port"},
	} {
		t.Run(test.file, func(t *testing.T) {
			path := filepath.Join("testdata", "contracts", test.file)
			before, err := os.ReadFile(path) //nolint:gosec // Path selects a hardcoded contract fixture.
			if err != nil {
				t.Fatal(err)
			}
			manifest, project, err := compose.Load(t.Context(), path, nil)
			if test.field == "" {
				if err != nil {
					t.Fatalf("%v (cause: %v)", err, errors.Unwrap(err))
				}
				if manifest == nil || project == nil || len(project.Services) != 2 {
					t.Fatal("missing loaded project")
				}
				if _, err := generated.Validate(t.Context(), manifest); err != nil {
					t.Fatal(err)
				}
			} else {
				var validation *config.Error
				if !errors.As(err, &validation) || !strings.HasPrefix(validation.Field, test.field) {
					t.Fatalf("error = %v, want field %s", err, test.field)
				}
				if manifest != nil || project != nil {
					t.Fatal("failed load returned partial results")
				}
			}
			after, err := os.ReadFile(path) //nolint:gosec // Path selects the same hardcoded fixture.
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("fixture was modified")
			}
		})
	}
}

func TestSDKLoadingSemantics(t *testing.T) {
	cleanEnvironment(t)
	directory := t.TempDir()
	files := map[string]string{
		"trocker.yaml": "version: 1\ncompose: [base.yaml, override.yaml]\nendpoints: {api: {service: backend, port: 8000, scheme: http}}\n",
		".env":         "TROCKER_TEST_IMAGE=nginx:alpine\nCOMPOSE_PROFILES=debug\n",
		"base.yaml": `name: fixture
include:
  - included.yaml
services:
  backend:
    image: ${TROCKER_TEST_IMAGE:?image required}
    expose: ["8000"]
    environment:
      KEEP: original
  debugger:
    image: busybox
    profiles: [debug]
`,
		"override.yaml": "services:\n  backend:\n    environment:\n      ADDED: override\n",
		"included.yaml": "services:\n  worker:\n    image: busybox\n",
	}
	for name, content := range files {
		writeFile(t, directory, name, content)
	}
	// An explicit working directory must isolate .env loading from the caller.
	callerDirectory := t.TempDir()
	writeFile(t, callerDirectory, ".env", "TROCKER_TEST_IMAGE=wrong:latest\n")
	t.Chdir(callerDirectory)
	_, project, err := compose.Load(t.Context(), filepath.Join(directory, "trocker.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	backend := project.Services["backend"]
	if backend.Image != "nginx:alpine" || backend.Environment["KEEP"] == nil || *backend.Environment["KEEP"] != "original" || backend.Environment["ADDED"] == nil || *backend.Environment["ADDED"] != "override" {
		t.Fatal("SDK interpolation or override merge was not preserved")
	}
	if _, ok := project.Services["worker"]; !ok {
		t.Fatal("include missing")
	}
	if _, ok := project.Services["debugger"]; !ok {
		t.Fatal(".env profile was not activated")
	}
	for name, before := range files {
		after, err := os.ReadFile(filepath.Join(directory, name)) //nolint:gosec // Names are hardcoded and directory is test-owned.
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != before {
			t.Fatalf("%s was modified", name)
		}
	}
}

func TestSDKPortNormalizationAndProfiles(t *testing.T) {
	cleanEnvironment(t)
	for _, test := range []struct{ name, port, field string }{
		{name: "expose TCP range", port: "expose: ['7999-8001/tcp']"},
		{name: "published port", port: "ports: ['24000:8000']"},
		{name: "inactive service", port: "expose: ['8000']\n    profiles: [debug]", field: "endpoints.api.service"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeFile(t, directory, "compose.yaml", "name: fixture\nservices:\n  backend:\n    image: busybox\n    "+test.port+"\n")
			path := writeFile(t, directory, "trocker.yaml", "version: 1\ncompose: [compose.yaml]\nendpoints: {api: {service: backend, port: 8000, scheme: http}}\n")
			_, _, err := compose.Load(t.Context(), path, nil)
			if test.field == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var validation *config.Error
				if !errors.As(err, &validation) || validation.Field != test.field {
					t.Fatalf("error = %v, want field %s", err, test.field)
				}
			}
		})
	}
}

func TestManifestProfileSelection(t *testing.T) {
	const profileCompose = `name: fixture
services:
  always:
    image: busybox
  debugger:
    image: busybox
    profiles: [debug]
  other:
    image: busybox
    profiles: [other]
`
	for _, test := range []struct {
		name     string
		profiles string
		env      string
		override []string
		want     []string
	}{
		{name: "manifest replaces environment", profiles: "profiles: [debug]\n", env: "other", want: []string{"always", "debugger"}},
		{name: "environment without manifest", env: "debug", want: []string{"always", "debugger"}},
		{name: "empty manifest preserves environment", profiles: "profiles: []\n", env: "debug", want: []string{"always", "debugger"}},
		{name: "explicit override replaces manifest", profiles: "profiles: [debug]\n", override: []string{"other"}, want: []string{"always", "other"}},
		{name: "manifest replaces wildcard environment", profiles: "profiles: [debug]\n", env: "*", want: []string{"always", "debugger"}},
		{name: "wildcard activates all", profiles: "profiles: ['*']\n", env: "other", want: []string{"always", "debugger", "other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cleanEnvironment(t)
			directory := t.TempDir()
			writeFile(t, directory, "compose.yaml", profileCompose)
			path := writeFile(t, directory, "trocker.yaml", "version: 1\ncompose: [compose.yaml]\n"+test.profiles)
			if test.env != "" {
				t.Setenv("COMPOSE_PROFILES", test.env)
			}
			_, project, err := compose.Load(t.Context(), path, test.override)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(project.ServiceNames(), test.want) {
				t.Fatalf("active services = %v, want %v", project.ServiceNames(), test.want)
			}
		})
	}
}

func TestProfileGatedEndpointValidation(t *testing.T) {
	const composeContent = "name: fixture\nservices:\n  backend:\n    image: busybox\n    expose: ['8000']\n    profiles: [debug]\n"
	const endpoint = "endpoints: {api: {service: backend, port: 8000, scheme: http}}\n"
	for _, test := range []struct {
		name     string
		profiles string
		env      string
		valid    bool
	}{
		{name: "inactive without selection"},
		{name: "manifest selects profile", profiles: "profiles: [debug]\n", valid: true},
		{name: "environment selects profile", env: "debug", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cleanEnvironment(t)
			directory := t.TempDir()
			writeFile(t, directory, "compose.yaml", composeContent)
			path := writeFile(t, directory, "trocker.yaml", "version: 1\ncompose: [compose.yaml]\n"+test.profiles+endpoint)
			if test.env != "" {
				t.Setenv("COMPOSE_PROFILES", test.env)
			}
			_, _, err := compose.Load(t.Context(), path, nil)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var validation *config.Error
			if !errors.As(err, &validation) || validation.Field != "endpoints.api.service" {
				t.Fatalf("error = %v, want inactive endpoint service", err)
			}
			for _, want := range []string{"debug", "profiles in trocker.yaml", "active profiles: none"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not explain %q", err, want)
				}
			}
		})
	}
}

func TestUnknownExplicitProfileExplainsAvailableProfiles(t *testing.T) {
	cleanEnvironment(t)
	directory := t.TempDir()
	writeFile(t, directory, "compose.yaml", "name: fixture\nservices:\n  debugger:\n    image: busybox\n    profiles: [debug]\n")
	path := writeFile(t, directory, "trocker.yaml", "version: 1\ncompose: [compose.yaml]\nprofiles: [typo]\n")
	_, _, err := compose.Load(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "typo") || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("unexpected unknown profile error: %v", err)
	}
	var diagnostic interface{ Help() string }
	if !errors.As(err, &diagnostic) || !strings.Contains(diagnostic.Help(), "debug") {
		t.Fatalf("unknown profile has no available-profile guidance: %v", err)
	}
}

func TestFailuresAndCancellation(t *testing.T) {
	cleanEnvironment(t)
	for _, content := range []string{
		"services: [",
		"services:\n  api:\n    image: ${MISSING:?do-not-print}\n",
		"services:\n  api:\n    image: busybox\n    ports: [do-not-print]\n",
	} {
		directory := t.TempDir()
		composePath := writeFile(t, directory, "compose.yaml", content)
		manifestContent := "version: 1\ncompose: [compose.yaml]\n"
		path := writeFile(t, directory, "trocker.yaml", manifestContent)
		manifest, project, err := compose.Load(t.Context(), path, nil)
		if err == nil || strings.Contains(err.Error(), "do-not-print") {
			t.Fatalf("unexpected diagnostic: %v", err)
		}
		if manifest != nil || project != nil {
			t.Fatal("failed loading returned a usable-looking result")
		}
		for name, before := range map[string]string{path: manifestContent, composePath: content} {
			after, err := os.ReadFile(name) //nolint:gosec // Paths are test-owned input files.
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != before {
				t.Fatal("failed loading modified input files")
			}
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatal("failed loading created unexpected files")
		}
	}
	directory := t.TempDir()
	path := writeFile(t, directory, "trocker.yaml", "version: 1\ncompose: [missing.yaml]\n")
	if _, _, err := compose.Load(t.Context(), path, nil); err == nil || !strings.Contains(err.Error(), "a referenced Compose file does not exist") {
		t.Fatalf("missing Compose file diagnostic = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := compose.Load(ctx, path, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func TestDiscoveryExplainsMissingConfiguration(t *testing.T) {
	cleanEnvironment(t)
	directory := t.TempDir()
	project, err := compose.DiscoverFiles(t.Context(), directory)
	if err == nil || project != nil {
		t.Fatalf("discovery without configuration: project=%v, err=%v", project, err)
	}
	var diagnostic interface {
		Help() string
		Note() string
	}
	if !errors.As(err, &diagnostic) {
		t.Fatal("missing configuration error has no recovery guidance")
	}
	if err.Error() != "no Compose file found" || !strings.Contains(diagnostic.Help(), "compose.yaml") || !strings.Contains(diagnostic.Note(), "generate trocker.yaml") {
		t.Fatalf("incomplete diagnostic: %v, %q, %q", err, diagnostic.Help(), diagnostic.Note())
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed discovery changed checkout: %v, %v", entries, err)
	}
}

func cleanEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"COMPOSE_FILE", "COMPOSE_PROFILES", "COMPOSE_PROJECT_NAME", "COMPOSE_ENV_FILES", "COMPOSE_DISABLE_ENV_FILE", "COMPOSE_COMPATIBILITY", "TROCKER_TEST_IMAGE", "MISSING"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DOCKER_HOST", "unix:///nonexistent-trocker-test.sock")
	t.Setenv("DOCKER_CONTEXT", "nonexistent-trocker-context")
}

func writeFile(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
