//go:build linux || darwin

package lifecycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/cli"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/native"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type nativeReport struct {
	ID, Binding, ContainerOnly, Inherited, Body string
	Args                                        []string
	ConfigPath                                  string
	Config                                      map[string]string
}

func nativeArguments(t *testing.T) []string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{executable, "-test.run=^TestNativeLifecycleHelper$", "--", "a b", "", "--help", "--", "$HOME"}
}

func TestNativeRunWithoutEndpointReferencesIsOffline(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	for name, content := range map[string]string{
		"compose.yaml": "services:\n  app:\n    image: busybox:1.37\n    environment: {TROCKER_TEST_CONTAINER_ONLY: secret-container}\n",
		"trocker.yaml": "version: 1\ncompose: [compose.yaml]\nbindings:\n  app:\n    environment: {TROCKER_TEST_BINDING: container-binding}\n    native:\n      environment: {TROCKER_TEST_BINDING: '{{ workspace.id }}'}\n",
	} {
		if err := os.WriteFile(filepath.Join(main, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
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
	t.Setenv("TROCKER_TEST_INHERITED", "parent")
	t.Setenv("TROCKER_TEST_BINDING", "parent-binding")
	t.Setenv("TROCKER_WORKSPACE_ID", "imposter")
	t.Setenv("TROCKER_TEST_CONTAINER_ONLY", "")
	before, err := os.ReadFile(filepath.Join(store.Directory, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if err := lifecycle.Run(t.Context(), main, store, "app", nativeArguments(t), native.Streams{Out: &output, Err: &diagnostics}); err != nil {
		t.Fatal(err)
	}
	var received nativeReport
	if err := json.Unmarshal(output.Bytes(), &received); err != nil {
		t.Fatal(err)
	}
	if received.ID != registration.ID || received.Binding != registration.ID || received.ContainerOnly != "" || received.Inherited != "parent" {
		t.Fatalf("native precedence/identity: %+v", received)
	}
	if !slices.Equal(received.Args, []string{"a b", "", "--help", "--", "$HOME"}) {
		t.Fatalf("arguments changed: %q", received.Args)
	}
	after, err := os.ReadFile(filepath.Join(store.Directory, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("run rewrote registry")
	}
	if err := lifecycle.Run(t.Context(), main, store, "missing", nativeArguments(t), native.Streams{Out: io.Discard, Err: io.Discard}); err == nil {
		t.Fatal("unknown service launched")
	}

	assertNativeDoesNotHoldLocks(t, main, store, checkout.GitDir)

	// Exercise Cobra's separator and child exit classification with isolated state.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", home)
	defaultDirectory, err := state.DefaultDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(defaultDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultDirectory, "registry.json"), before, 0o600); err != nil { //nolint:gosec // HOME and XDG_STATE_HOME point to this test’s temporary directory.
		t.Fatal(err)
	}
	t.Chdir(main)
	t.Setenv("TROCKER_TEST_NATIVE_EXIT", "2")
	args := append([]string{"run", "app", "--"}, nativeArguments(t)...)
	err = cli.RunContext(t.Context(), args, "test", bytes.NewReader(nil), io.Discard, io.Discard)
	child, ok := errors.AsType[*native.ExitError](err)
	if !ok || child.Code != 2 || errors.Is(err, cli.ErrUsage) {
		t.Fatalf("child exit 2 became a CLI error: %v", err)
	}
}

func TestNativeLifecycleHelper(t *testing.T) {
	if os.Getenv("TROCKER_TEST_NATIVE_LIFECYCLE") == "" {
		return
	}
	result := nativeReport{ID: os.Getenv("TROCKER_WORKSPACE_ID"), Binding: os.Getenv("TROCKER_TEST_BINDING"), ContainerOnly: os.Getenv("TROCKER_TEST_CONTAINER_ONLY"), Inherited: os.Getenv("TROCKER_TEST_INHERITED")}
	index := slices.Index(os.Args, "--")
	if index >= 0 {
		result.Args = os.Args[index+1:]
	}
	address := os.Getenv("TROCKER_TEST_API_URL")
	var originalConfig []byte
	if os.Getenv("TROCKER_TEST_NATIVE_FILE") == "1" {
		result.ConfigPath = os.Getenv("APP_CONFIG")
		data, err := os.ReadFile(result.ConfigPath) //nolint:gosec // Parent supplies a private generated fixture path.
		if err != nil {
			os.Exit(97)
		}
		originalConfig = data
		if err := json.Unmarshal(data, &result.Config); err != nil {
			os.Exit(98)
		}
		address = result.Config["apiUrl"]
	}
	if address != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/token", nil) //nolint:gosec // Helper receives the owned Docker fixture’s observed endpoint.
		if err != nil {
			os.Exit(90)
		}
		response, err := http.DefaultClient.Do(request) //nolint:gosec // Calls only the test-owned endpoint supplied by the parent.
		if err != nil {
			os.Exit(91)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			os.Exit(92)
		}
		if err := response.Body.Close(); err != nil {
			os.Exit(93)
		}
		if response.StatusCode != http.StatusOK {
			os.Exit(94)
		}
		result.Body = string(body)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(95)
	}
	if os.Getenv("TROCKER_TEST_NATIVE_WAIT") == "1" {
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(96)
		}
		if result.ConfigPath != "" {
			data, err := os.ReadFile(result.ConfigPath) //nolint:gosec // Reopen this child’s original generated snapshot after another publication.
			if err != nil || !bytes.Equal(data, originalConfig) {
				os.Exit(99)
			}
		}
	}
	if os.Getenv("TROCKER_TEST_NATIVE_EXIT") == "2" {
		os.Exit(2)
	}
	os.Exit(0)
}

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

func assertNativeDoesNotHoldLocks(t *testing.T, main string, store state.Store, gitDir string) {
	t.Helper()
	t.Setenv("TROCKER_TEST_NATIVE_WAIT", "1")
	defer t.Setenv("TROCKER_TEST_NATIVE_WAIT", "")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	input, send := io.Pipe()
	output, receive := io.Pipe()
	defer func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	}()
	defer func() {
		if err := send.Close(); err != nil {
			t.Error(err)
		}
	}()
	args := nativeArguments(t)
	done := make(chan error, 1)
	go func() {
		err := lifecycle.Run(ctx, main, store, "app", args, native.Streams{In: input, Out: receive, Err: io.Discard})
		done <- errors.Join(err, receive.Close())
	}()
	var ready nativeReport
	if err := json.NewDecoder(output).Decode(&ready); err != nil {
		t.Fatal("child readiness", err)
	}
	locking, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	lock, err := store.LockWorkspace(locking, gitDir)
	if err != nil {
		t.Fatal("native lifetime blocked workspace mutations", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(locking, func(*state.Registry) error { return nil }); err != nil {
		t.Fatal("native lifetime blocked registry", err)
	}
	if ready.ConfigPath != "" {
		data := readNativeConfig(t, ready.ConfigPath)
		paths, err := store.PublishGenerated(locking, ready.ID, "", "app", map[string][]byte{filepath.Base(ready.ConfigPath): data})
		if err != nil || paths[filepath.Base(ready.ConfigPath)] != ready.ConfigPath {
			t.Fatal("native lifetime blocked generation publication", err)
		}
		changed, err := store.PublishGenerated(locking, ready.ID, "", "app", map[string][]byte{filepath.Base(ready.ConfigPath): append(data, '\n')})
		if err != nil || changed[filepath.Base(ready.ConfigPath)] == ready.ConfigPath {
			t.Fatal("changed publication failed during native lifetime", err)
		}
	}
	if err := send.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
