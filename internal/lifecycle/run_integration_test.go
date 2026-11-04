//go:build integration && (linux || darwin)

package lifecycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/native"
)

func TestNativeRunUsesObservedDependencyWithoutMutations(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	manifest := `version: 1
compose: [compose.yaml]
endpoints:
  api: {service: api, port: 8000, scheme: http}
bindings:
  api:
    native:
      environment:
        TROCKER_TEST_API_URL: "{{ endpoints.api.host.url }}"
        TROCKER_TEST_BINDING: "{{ workspace.id }}"
`
	if err := os.WriteFile(filepath.Join(main, "trocker.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TROCKER_TEST_NATIVE_LIFECYCLE", "1")
	t.Setenv("TROCKER_WORKSPACE_ID", "imposter")
	var output bytes.Buffer
	if err := lifecycle.Run(ctx, main, store, "api", nativeArguments(t), native.Streams{Out: &output, Err: io.Discard}); err == nil || output.Len() != 0 {
		t.Fatal("missing dependency launched child", err)
	}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	token := readEndpointToken(t, ctx, result)
	before := snapshotObservationFiles(t, main, store.Directory)
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Run(ctx, main, store, "api", nativeArguments(t), native.Streams{Out: &output, Err: io.Discard}); err != nil {
		t.Fatal(err)
	}
	var received nativeReport
	if err := json.Unmarshal(output.Bytes(), &received); err != nil {
		t.Fatal(err)
	}
	if received.ID != registration.ID || received.Binding != registration.ID || received.Body != token {
		t.Fatalf("native dependency resolution: %+v", received)
	}
	after, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != containers[0].ID || !reflect.DeepEqual(before, snapshotObservationFiles(t, main, store.Directory)) {
		t.Fatal("native run changed files or resources")
	}
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Stop}, io.Discard); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := lifecycle.Run(ctx, main, store, "api", nativeArguments(t), native.Streams{Out: &output, Err: io.Discard}); err == nil || output.Len() != 0 {
		t.Fatal("stopped dependency launched child", err)
	}
}

func TestGeneratedFileClientUsesObservedEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	manifest := strings.Replace(nativeFileManifest, "bindings:", "endpoints:\n  api: {service: api, port: 8000, scheme: http}\nbindings:", 1)
	manifest = strings.Replace(manifest, "  app:", "  api:", 1)
	manifest = strings.Replace(manifest, "            workspace_id:", "            api_url: '{{ endpoints.api.host.url }}'\n            workspace_id:", 1)
	if err := os.WriteFile(filepath.Join(main, "trocker.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(main, ".trocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, ".trocker", "app.json.tmpl"), []byte(`{"workspaceId":{{json .workspace_id}},"apiUrl":{{json .api_url}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TROCKER_TEST_NATIVE_LIFECYCLE", "1")
	t.Setenv("TROCKER_TEST_NATIVE_FILE", "1")
	var output bytes.Buffer
	run := func() error {
		return lifecycle.Run(ctx, main, store, "api", nativeArguments(t), native.Streams{Out: &output, Err: io.Discard})
	}
	if err := run(); err == nil || output.Len() != 0 {
		t.Fatal("missing file dependency launched a child", err)
	}
	if _, err := os.Stat(filepath.Join(store.Directory, "generated")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing dependency wrote generated files", err)
	}
	started, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	token := readEndpointToken(t, ctx, started)
	before := snapshotObservationFiles(t, main, store.Directory)
	preview, err := lifecycle.Plan(ctx, main, store)
	if err != nil || preview.NativeFiles != 1 || !reflect.DeepEqual(before, snapshotObservationFiles(t, main, store.Directory)) {
		t.Fatal("plan missed native template or changed files", err)
	}
	if _, err := os.Stat(filepath.Join(store.Directory, "generated")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("up/plan rendered native files", err)
	}
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	var received nativeReport
	if err := json.Unmarshal(output.Bytes(), &received); err != nil {
		t.Fatal(err)
	}
	if received.Config["apiUrl"] != started.Endpoints[0].Addresses.Host.URL || received.Config["workspaceId"] != registration.ID || received.Body != token || !strings.Contains(received.ConfigPath, "target-") {
		t.Fatal("generated file did not deliver the observed endpoint")
	}
	current, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil || len(current) != 1 || current[0].ID != containers[0].ID {
		t.Fatal("native client mutated the Docker stack", err)
	}
	snapshot := snapshotObservationFiles(t, store.Directory)
	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Stop}, io.Discard); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(); err == nil || output.Len() != 0 {
		t.Fatal("stopped file dependency launched a child", err)
	}
	if !reflect.DeepEqual(snapshot, snapshotObservationFiles(t, store.Directory)) {
		t.Fatal("failed observation changed generated state")
	}
}
