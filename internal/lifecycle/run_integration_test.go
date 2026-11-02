//go:build integration && (linux || darwin)

package lifecycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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
	containers, err := target.Containers(ctx, registration)
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
	after, err := target.Containers(ctx, registration)
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
