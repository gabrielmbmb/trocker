//go:build integration

package lifecycle_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected path %s: %v", path, err)
	}
}

// These tests query the selected local daemon but never create Docker resources.
func TestInitRegistersAndRetryKeepsTheWorkspace(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	manifestPath := filepath.Join(main, "trocker.yaml")
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte("services:\n  api:\n    image: busybox\n    expose: ['8000']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proposal, err := lifecycle.PrepareInit(t.Context(), main, store)
	if err != nil || proposal.TargetID == "" || len(proposal.Manifest) == 0 {
		t.Fatalf("proposal = %+v, %v", proposal, err)
	}
	assertAbsent(t, store.Directory)
	assertAbsent(t, manifestPath)

	first, err := lifecycle.Init(t.Context(), main, store)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(manifestPath) //nolint:gosec // Test-owned manifest.
	if err != nil {
		t.Fatal(err)
	}
	retry, err := lifecycle.PrepareInit(t.Context(), main, store)
	if err != nil || retry.ExistingID != first.ID || len(retry.Manifest) != 0 || retry.NewTarget {
		t.Fatalf("retry proposal did not preserve registration/configuration: %+v, %v", retry, err)
	}
	second, err := lifecycle.Init(t.Context(), main, store)
	if err != nil || second.ID != first.ID {
		t.Fatalf("retry: %+v, %v", second, err)
	}
	after, err := os.ReadFile(manifestPath) //nolint:gosec // Test-owned manifest.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("init rewrote existing manifest")
	}
}

// Init validates Compose even when the manifest declares an environment
// wrapper: run outside it, interpolation fails and nothing is registered.
func TestInitValidatesComposeDespiteAWrapper(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	for name, content := range map[string]string{
		"compose.yaml": "services:\n  api:\n    image: busybox\n    environment: {VALUE: '${FROM_WRAPPER:?}'}\n",
		"trocker.yaml": "version: 1\ncompose: [compose.yaml]\nenvironment_wrapper: [env.sh]\n",
		"env.sh":       "#!/bin/sh\nFROM_WRAPPER=yes exec \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(main, name), []byte(content), 0o700); err != nil { //nolint:gosec // The wrapper must be executable.
			t.Fatal(err)
		}
	}
	if _, err := lifecycle.Init(t.Context(), main, store); err == nil {
		t.Fatal("init registered a workspace whose Compose files do not load")
	}
	assertAbsent(t, store.Directory)
}
