package lifecycle_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// Releasing something that is already gone must succeed, so scripts and agents
// can release unconditionally instead of parsing error messages.
func TestReleaseUnknownWorkspaceIsANoop(t *testing.T) {
	directory := t.TempDir()
	store := state.Store{Directory: filepath.Join(directory, "state")}
	root := filepath.Join(directory, "other")
	if err := store.Update(t.Context(), func(registry *state.Registry) error {
		registry.Workspaces = append(registry.Workspaces, workspace.Registration{
			ID:       uuid.NewString(),
			Checkout: workspace.Checkout{Root: root, GitDir: filepath.Join(root, ".git"), CommonDir: filepath.Join(root, ".git")},
			Targets:  []string{"daemon"},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(store.Directory, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Release(t.Context(), directory, store, uuid.NewString(), false, io.Discard)
	if err != nil {
		t.Fatalf("unknown workspace: %v", err)
	}
	if result.WorkspaceID != "" || result.Released || !result.Held.Empty() {
		t.Fatalf("result = %+v", result)
	}
	after, err := os.ReadFile(filepath.Join(store.Directory, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("no-op release rewrote the registry")
	}
}

func TestReleaseRejectsAmbiguousReference(t *testing.T) {
	store := state.Store{Directory: filepath.Join(t.TempDir(), "state")}
	// Two valid ids sharing the "abcdef01" prefix.
	ids := []string{"abcdef01-0000-4000-8000-000000000001", "abcdef01-0000-4000-8000-000000000002"}
	if err := store.Update(t.Context(), func(registry *state.Registry) error {
		for _, id := range ids {
			root := filepath.Join(t.TempDir(), id)
			registry.Workspaces = append(registry.Workspaces, workspace.Registration{
				ID:       id,
				Checkout: workspace.Checkout{Root: root, GitDir: filepath.Join(root, ".git"), CommonDir: filepath.Join(root, ".git")},
				Targets:  []string{"daemon"},
			})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := lifecycle.Release(t.Context(), t.TempDir(), store, "abcdef01", false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "matches 2 registrations") {
		t.Fatalf("ambiguous release: %v", err)
	}
}

func TestReleaseUnregisteredWorkspaceNeedsNoDocker(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	_, err := lifecycle.Release(t.Context(), main, store, "", false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("release in an unregistered worktree: %v", err)
	}
	if _, err := os.Stat(store.Directory); !os.IsNotExist(err) {
		t.Fatalf("release created state in an unregistered worktree: %v", err)
	}
}
