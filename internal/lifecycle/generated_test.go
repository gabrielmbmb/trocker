//go:build linux || darwin

package lifecycle_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/native"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

const nativeFileManifest = `version: 1
compose: [compose.yaml]
bindings:
  app:
    native:
      files:
        config:
          template: .trocker/app.json.tmpl
          output: app.json
          format: json
          values:
            workspace_id: "{{ workspace.id }}"
      environment:
        APP_CONFIG: "{{ files.config.path }}"
`

func TestNativeGeneratedConfigurationSnapshots(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "private state")}
	if err := os.MkdirAll(filepath.Join(main, ".trocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"compose.yaml": "services: {app: {image: busybox:1.37}}\n", "trocker.yaml": nativeFileManifest} {
		if err := os.WriteFile(filepath.Join(main, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	template := filepath.Join(main, ".trocker", "app.json.tmpl")
	if err := os.WriteFile(template, []byte(`{"workspaceId":{{json .workspace_id}},"message":"one"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checkout, err := workspace.Discover(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := lifecycle.Register(t.Context(), store, checkout, "offline-target")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("TROCKER_TEST_NATIVE_LIFECYCLE", "1")
	t.Setenv("TROCKER_TEST_NATIVE_FILE", "1")
	t.Setenv("APP_CONFIG", "must-be-overridden")
	subdirectory := filepath.Join(main, "subdirectory")
	if err := os.Mkdir(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func() (nativeReport, error) {
		t.Helper()
		var output bytes.Buffer
		err := lifecycle.Run(t.Context(), subdirectory, store, "app", nativeArguments(t), native.Streams{Out: &output, Err: io.Discard})
		var report nativeReport
		if err != nil {
			if output.Len() != 0 {
				t.Fatal("failed preparation launched a child")
			}
			return report, err
		}
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		return report, nil
	}
	first, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if first.Config["workspaceId"] != registration.ID || first.Config["message"] != "one" || !filepath.IsAbs(first.ConfigPath) || !strings.Contains(filepath.ToSlash(first.ConfigPath), "/local/native/") {
		t.Fatalf("wrong generated configuration: %+v", first)
	}
	original := readNativeConfig(t, first.ConfigPath)
	info, err := os.Stat(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := run()
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.Stat(second.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if second.ConfigPath != first.ConfigPath || !unchanged.ModTime().Equal(info.ModTime()) {
		t.Fatal("unchanged run rewrote generated configuration")
	}
	if err := os.WriteFile(template, []byte(`{"workspaceId":{{json .workspace_id}},"message":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if third.ConfigPath == first.ConfigPath || third.Config["message"] != "two" || !bytes.Equal(original, readNativeConfig(t, first.ConfigPath)) {
		t.Fatal("changed configuration did not preserve the previous snapshot")
	}
	assertNativeDoesNotHoldLocks(t, main, store, checkout.GitDir)
	index := filepath.Join(filepath.Dir(filepath.Dir(third.ConfigPath)), "current.json")
	previousIndex := readNativeConfig(t, index)
	for _, invalid := range []string{"{{ DO_NOT_PRINT_SECRET }}", "DO_NOT_PRINT_SECRET {{.workspace_id}}", "{{.missing}}"} {
		if err := os.WriteFile(template, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := run(); err == nil || strings.Contains(err.Error(), "DO_NOT_PRINT_SECRET") {
			t.Fatal("invalid template launched or leaked content", err)
		}
		if !bytes.Equal(previousIndex, readNativeConfig(t, index)) {
			t.Fatal("render failure changed selected generation")
		}
	}
	// Plan/init check syntax before requiring a daemon, but never render files.
	if err := os.WriteFile(template, []byte("{{ DO_NOT_PRINT_SECRET }}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Plan(t.Context(), main, store); err == nil || !strings.Contains(err.Error(), "template syntax") {
		t.Fatal("plan missed invalid template", err)
	}
	called := false
	if _, err := lifecycle.Init(t.Context(), main, store, func(lifecycle.InitProposal) (bool, error) { called = true; return true, nil }); err == nil || called {
		t.Fatal("init did not validate templates before confirmation", err)
	}
	if _, err := os.Stat(filepath.Join(main, "app.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("generated output entered the checkout", err)
	}
}

func readNativeConfig(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // Test-owned generated files.
	if err != nil {
		t.Fatal(err)
	}
	return data
}
