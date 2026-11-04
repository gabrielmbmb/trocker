//go:build integration

package lifecycle_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// A released workspace must leave no registration, no port preference, and no
// owned resource behind, and it must stop being listed.
func TestReleaseStopsAndForgetsWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Release(ctx, main, store, "", true, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != registration.ID || !result.Released || result.Held.Containers == 0 || result.Held.Volumes == 0 {
		t.Fatalf("result = %+v", result)
	}
	if held := heldResources(t, ctx, target, registration); !held.Empty() {
		t.Fatalf("resources after release = %+v", held)
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 0 || len(registry.Ports) != 0 {
		t.Fatalf("registry after release = %+v, %v", registry, err)
	}
	if marker, err := workspace.Identity(registration.Checkout); err != nil || marker != "" {
		t.Fatalf("identity marker after release = %q, %v", marker, err)
	}
	if stacks, err := lifecycle.ListStacks(ctx, store, true); err != nil || len(stacks) != 0 {
		t.Fatalf("stacks after release = %+v, %v", stacks, err)
	}
}

// Retained volumes are data. A release without --volumes must refuse before it
// changes anything, so the containers still describe what is running.
func TestReleaseRefusesRetainedVolumesBeforeChangingAnything(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	before := heldResources(t, ctx, target, registration)
	_, err := lifecycle.Release(ctx, main, store, "", false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "still owns") {
		t.Fatalf("release without --volumes: %v", err)
	}
	if after := heldResources(t, ctx, target, registration); after != before {
		t.Fatalf("refused release changed resources: %+v -> %+v", before, after)
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 1 {
		t.Fatalf("refused release changed the registry: %+v, %v", registry, err)
	}
	if marker, err := workspace.Identity(registration.Checkout); err != nil || marker != registration.ID {
		t.Fatalf("refused release removed the marker: %q, %v", marker, err)
	}
	if _, err := lifecycle.Release(ctx, main, store, "", true, io.Discard); err != nil {
		t.Fatal(err)
	}
}

// The worktree can be gone while the stack still runs: the registration is the
// only handle on those containers, and release must work from another directory.
func TestReleaseFromRegistryAfterWorktreeRemoval(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, _, target := upWorkspace(t, ctx)
	linked, registration := linkedWorkspace(t, ctx, main, store, target)
	if _, err := lifecycle.Up(ctx, linked, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	git(t, main, "worktree", "remove", "--force", linked)

	// Reference an id prefix from the main checkout, not the deleted worktree.
	result, err := lifecycle.Release(ctx, main, store, registration.ID[:8], true, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != registration.ID || !result.Released {
		t.Fatalf("result = %+v", result)
	}
	if held := heldResources(t, ctx, target, registration); !held.Empty() {
		t.Fatalf("resources after release = %+v", held)
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 1 {
		t.Fatalf("registry after release = %+v, %v", registry.Workspaces, err)
	}
}

// Registration tracks worktrees, not stacks. init releases registrations that
// Git no longer records and that own nothing; anything else waits for an
// explicit release decision.
func TestInitReleasesOnlyInertDeadWorkspaces(t *testing.T) {
	for _, test := range []struct {
		name           string
		up             bool
		remove         func(t *testing.T, main, linked string)
		wantRegistered bool
		wantStatus     string
		wantGitMeta    bool
	}{
		{
			name:   "worktree removed by git and owns nothing",
			remove: removeWorktree,
		},
		{
			name:           "worktree removed while the stack runs",
			up:             true,
			remove:         removeWorktree,
			wantRegistered: true,
			wantStatus:     "running",
		},
		{
			name:           "directory deleted without git pruning",
			remove:         removeDirectory,
			wantRegistered: true,
			wantStatus:     "git metadata survives",
			wantGitMeta:    true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			main, store, _, target := upWorkspace(t, ctx)
			linked, registration := linkedWorkspace(t, ctx, main, store, target)
			if test.up {
				if _, err := lifecycle.Up(ctx, linked, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			test.remove(t, main, linked)

			if _, err := lifecycle.Init(ctx, main, store, func(lifecycle.InitProposal) (bool, error) { return true, nil }); err != nil {
				t.Fatal(err)
			}
			registry, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			found := registered(registry, registration.ID)
			if found != test.wantRegistered {
				t.Fatalf("registration kept = %v, want %v", found, test.wantRegistered)
			}
			if !found {
				return
			}
			stacks, err := lifecycle.ListStacks(ctx, store, true)
			if err != nil {
				t.Fatal(err)
			}
			stack := findStack(stacks, registration.ID)
			if stack == nil || !stack.Missing || stack.GitMetadata != test.wantGitMeta || !strings.Contains(stack.Status, test.wantStatus) {
				t.Fatalf("stack = %+v", stack)
			}
			if _, err := lifecycle.Release(ctx, main, store, registration.ID, true, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func linkedWorkspace(t *testing.T, ctx context.Context, main string, store state.Store, target *compose.Target) (string, workspace.Registration) {
	t.Helper()
	linked := filepath.Join(filepath.Dir(main), "linked")
	git(t, main, "worktree", "add", "--detach", linked) //nolint:contextcheck // Git fixtures use the test context; daemon operations use the shorter timeout.
	for name, content := range map[string]string{
		"compose.yaml": upCompose,
		"trocker.yaml": "version: 1\ncompose: [compose.yaml]\n",
	} {
		if err := os.WriteFile(filepath.Join(linked, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checkout, err := workspace.Discover(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := lifecycle.Register(ctx, store, checkout, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanupProject(t, cleanup, target, registration)
	})
	return linked, registration
}

func heldResources(t *testing.T, ctx context.Context, target *compose.Target, registration workspace.Registration) compose.Held {
	t.Helper()
	containers, err := target.Containers(ctx, registration, api.PsOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	networks, volumes, err := target.Retained(ctx, registration)
	if err != nil {
		t.Fatal(err)
	}
	return compose.Held{Containers: len(containers), Networks: networks, Volumes: volumes}
}

func registered(registry state.Registry, id string) bool {
	for _, registration := range registry.Workspaces {
		if registration.ID == id {
			return true
		}
	}
	return false
}

func findStack(stacks []lifecycle.Stack, id string) *lifecycle.Stack {
	for i, stack := range stacks {
		if stack.WorkspaceID == id {
			return &stacks[i]
		}
	}
	return nil
}

func removeWorktree(t *testing.T, main, linked string) {
	t.Helper()
	git(t, main, "worktree", "remove", "--force", linked)
}

func removeDirectory(t *testing.T, _, linked string) {
	t.Helper()
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
}
