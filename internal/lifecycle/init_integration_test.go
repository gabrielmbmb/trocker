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
func TestInitConfirmationAndRetry(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	manifestPath := filepath.Join(main, "trocker.yaml")
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte("services:\n  api:\n    image: busybox\n    expose: ['8000']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	confirm := func(p lifecycle.InitProposal) (bool, error) {
		if p.TargetID == "" || len(p.Manifest) == 0 {
			t.Fatal("incomplete proposal")
		}
		return false, nil
	}
	if _, err := lifecycle.Init(t.Context(), main, store, confirm); !errors.Is(err, lifecycle.ErrDeclined) {
		t.Fatalf("decline: %v", err)
	}
	assertAbsent(t, store.Directory)
	assertAbsent(t, manifestPath)
	accepted := func(lifecycle.InitProposal) (bool, error) { return true, nil }
	first, err := lifecycle.Init(t.Context(), main, store, accepted)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(manifestPath) //nolint:gosec // Test-owned manifest.
	if err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Init(t.Context(), main, store, func(p lifecycle.InitProposal) (bool, error) {
		if p.ExistingID != first.ID || len(p.Manifest) != 0 || p.NewTarget {
			t.Fatal("retry did not preserve registration/configuration")
		}
		return true, nil
	})
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

func TestInitDoesNotOverwriteConcurrentManifest(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "created during confirmation"
		if existing {
			name = "modified during confirmation"
		}
		t.Run(name, func(t *testing.T) {
			directory, main := repository(t)
			store := state.Store{Directory: filepath.Join(directory, "state")}
			if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte("services: {api: {image: busybox}}"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(main, "trocker.yaml")
			if existing {
				if err := os.WriteFile(path, []byte("version: 1\ncompose: [compose.yaml]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			content := []byte("do not overwrite")
			_, err := lifecycle.Init(t.Context(), main, store, func(lifecycle.InitProposal) (bool, error) {
				if err := os.WriteFile(path, content, 0o600); err != nil {
					t.Fatal(err)
				}
				return true, nil
			})
			if err == nil {
				t.Fatal("concurrent manifest was accepted/overwritten")
			}
			after, err := os.ReadFile(path) //nolint:gosec // Test-owned manifest.
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, content) {
				t.Fatal("concurrent manifest was overwritten")
			}
			registry, err := store.Read()
			if err != nil || len(registry.Workspaces) != 0 {
				t.Fatalf("failed init registered workspace: %+v %v", registry, err)
			}
		})
	}
}
