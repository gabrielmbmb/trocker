//go:build integration

package lifecycle_test

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestListStacksReportsWorktreesAndState(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)

	linked := filepath.Join(filepath.Dir(main), "linked")
	git(t, main, "worktree", "add", "--detach", linked)
	linkedCheckout, err := workspace.Discover(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	linkedRegistration, err := lifecycle.Register(ctx, store, linkedCheckout, target.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A registration on another daemon must not appear in this target's list.
	otherDirectory := t.TempDir()
	other := workspace.Registration{
		ID: uuid.NewString(),
		Checkout: workspace.Checkout{
			Root:      filepath.Join(otherDirectory, "checkout"),
			GitDir:    filepath.Join(otherDirectory, "checkout", ".git"),
			CommonDir: filepath.Join(otherDirectory, "checkout", ".git"),
		},
		Targets: []string{"other-target"},
	}
	if err := store.Update(ctx, func(registry *state.Registry) error {
		registry.Workspaces = append(registry.Workspaces, other)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	before := snapshotObservationFiles(t, main, linked, store.Directory)
	stacks, err := lifecycle.ListStacks(ctx, store, false)
	if err != nil || len(stacks) != 0 {
		t.Fatalf("running stacks before up = %+v, %v", stacks, err)
	}
	stacks, err = lifecycle.ListStacks(ctx, store, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 2 || stacks[0].WorkspaceID != registration.ID || stacks[0].Worktree != registration.Checkout.Root || stacks[0].Status != "not created" ||
		stacks[1].WorkspaceID != linkedRegistration.ID || stacks[1].Worktree != linkedCheckout.Root || stacks[1].Status != "not created" {
		t.Fatalf("registered stacks = %+v", stacks)
	}
	after := snapshotObservationFiles(t, main, linked, store.Directory)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("listing changed local inputs")
	}

	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	stacks, err = lifecycle.ListStacks(ctx, store, false)
	if err != nil || len(stacks) != 1 || stacks[0].WorkspaceID != registration.ID || stacks[0].Status != "running(1)" {
		t.Fatalf("running stacks after up = %+v, %v", stacks, err)
	}

	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Stop}, io.Discard); err != nil {
		t.Fatal(err)
	}
	stacks, err = lifecycle.ListStacks(ctx, store, false)
	if err != nil || len(stacks) != 0 {
		t.Fatalf("running stacks after stop = %+v, %v", stacks, err)
	}
	stacks, err = lifecycle.ListStacks(ctx, store, true)
	if err != nil || len(stacks) != 2 || stacks[0].WorkspaceID != registration.ID || stacks[0].Status != "exited(1)" || stacks[1].Status != "not created" {
		t.Fatalf("all stacks after stop = %+v, %v", stacks, err)
	}
}
