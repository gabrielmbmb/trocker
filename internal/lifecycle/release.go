package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// ReleaseResult reports what releasing a workspace changed. An empty
// WorkspaceID means nothing matched, which is not an error: scripts and agents
// may release unconditionally.
type ReleaseResult struct {
	WorkspaceID string
	Held        compose.Held
	Released    bool
	// RemainingTargets is nonzero when another Docker target still depends on
	// the registration, so it was kept.
	RemainingTargets int
}

// Release stops whatever a workspace still owns on the selected target and then
// forgets its association. Retained volumes keep the registration unless
// volumes is set, because the registration is the only record that makes them
// removable later. Releasing nothing is a successful no-op.
func Release(ctx context.Context, directory string, store state.Store, reference string, volumes bool, output io.Writer) (result ReleaseResult, err error) {
	registration, found, err := releaseRegistration(ctx, directory, store, reference)
	if err != nil || !found {
		return result, err
	}
	lock, err := store.LockWorkspace(ctx, registration.Checkout.GitDir)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()

	// Reload under the workspace lock: a concurrent command may have released it.
	registration, found, err = registrationByReference(store, registration.ID)
	if err != nil || !found {
		return result, err
	}
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	if !slices.Contains(registration.Targets, target.ID) {
		return result, &registrationError{problem: "workspace is not registered with the selected Docker target"}
	}
	held, err := heldResources(ctx, target, registration)
	if err != nil {
		return result, err
	}
	result = ReleaseResult{WorkspaceID: registration.ID, Held: held}
	if !held.Empty() {
		// Refuse before the teardown: a partial release that stops containers and
		// then discovers retained data is harder to reason about than a refusal.
		if !volumes && held.Volumes > 0 {
			return result, &retainedError{held: held}
		}
		if err := target.ApplyLifecycle(ctx, registration, compose.LifecycleOptions{Operation: compose.Down, Volumes: volumes}, output); err != nil {
			return result, err
		}
		remaining, err := heldResources(ctx, target, registration)
		if err != nil {
			return result, err
		}
		if !remaining.Empty() {
			return result, &incompleteReleaseError{held: remaining}
		}
	}
	if err := removeIdentityMarker(registration); err != nil {
		return result, err
	}
	result.Released, result.RemainingTargets, err = releaseAssociation(ctx, store, registration.ID, target.ID)
	if err != nil {
		return result, err
	}
	return result, nil
}

// heldResources observes what a workspace still owns on one target, using the
// same ownership-checked container view as the lifecycle commands.
func heldResources(ctx context.Context, target *compose.Target, registration workspace.Registration) (compose.Held, error) {
	containers, err := target.Containers(ctx, registration)
	if err != nil {
		return compose.Held{}, err
	}
	networks, volumes, err := target.Retained(ctx, registration)
	if err != nil {
		return compose.Held{}, err
	}
	return compose.Held{Containers: len(containers), Networks: networks, Volumes: volumes}, nil
}

// releaseRegistration resolves the workspace to release. An empty reference
// means the current directory; otherwise the reference may be any unambiguous
// id prefix, which makes the value printed by ls sufficient.
func releaseRegistration(ctx context.Context, directory string, store state.Store, reference string) (workspace.Registration, bool, error) {
	if reference != "" {
		return registrationByReference(store, reference)
	}
	registration, err := registeredWorkspace(ctx, directory, store)
	if err != nil {
		return workspace.Registration{}, false, err
	}
	return registration, true, nil
}

func registrationByReference(store state.Store, reference string) (workspace.Registration, bool, error) {
	registry, err := store.Read()
	if err != nil {
		return workspace.Registration{}, false, err
	}
	matches := make([]workspace.Registration, 0, 1)
	for _, registration := range registry.Workspaces {
		if strings.HasPrefix(registration.ID, reference) {
			matches = append(matches, registration)
		}
	}
	switch len(matches) {
	case 0:
		return workspace.Registration{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return workspace.Registration{}, false, fmt.Errorf("workspace reference %q matches %d registrations; use more characters", reference, len(matches))
	}
}

// removeIdentityMarker deletes the worktree marker before the registry row. A
// marker without a row blocks registration with no supported recovery, while a
// row without its marker is repaired by init, so this is the recoverable order.
func removeIdentityMarker(registration workspace.Registration) error {
	marker, err := workspace.Identity(registration.Checkout)
	if err != nil {
		return err
	}
	switch marker {
	case "":
		return nil
	case registration.ID:
		if err := os.Remove(filepath.Join(registration.Checkout.GitDir, workspace.IdentityFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("workspace marker conflicts with the registration; explicit recovery required")
	}
}

func releaseAssociation(ctx context.Context, store state.Store, id, targetID string) (released bool, remaining int, err error) {
	err = store.Update(ctx, func(registry *state.Registry) error {
		released = registry.RemoveTarget(id, targetID)
		if index := slices.IndexFunc(registry.Workspaces, func(registration workspace.Registration) bool { return registration.ID == id }); !released && index >= 0 {
			remaining = len(registry.Workspaces[index].Targets)
		}
		return nil
	})
	return released, remaining, err
}

// reapDeadWorkspaces releases registrations whose worktree Git no longer
// records and which own nothing on this target, so that registration tracks
// workspaces instead of accumulating them. It runs where workspace
// administration already happens (init). A workspace that still owns anything
// is never touched: its registration is the only record that the resources
// belong to Trocker. Inspection failures are not proof of death, so they skip
// the registration and it is retried on the next init.
func reapDeadWorkspaces(ctx context.Context, store state.Store, target *compose.Target) error {
	registry, err := store.Read()
	if err != nil {
		return err
	}
	for _, registration := range registry.Workspaces {
		if !slices.Contains(registration.Targets, target.ID) {
			continue
		}
		liveness, err := workspace.CheckoutLiveness(registration.Checkout)
		if err != nil || liveness != workspace.CheckoutGone {
			continue
		}
		held, err := heldResources(ctx, target, registration)
		if err != nil || !held.Empty() {
			continue
		}
		if _, _, err := releaseAssociation(ctx, store, registration.ID, target.ID); err != nil {
			return err
		}
	}
	return nil
}

type retainedError struct{ held compose.Held }

func (e *retainedError) Error() string {
	return fmt.Sprintf("workspace still owns %s", e.held.Summary())
}
func (e *retainedError) Help() string { return "rerun with --volumes to remove retained data" }
func (e *retainedError) Note() string { return "registration retained; nothing was changed" }

type incompleteReleaseError struct{ held compose.Held }

func (e *incompleteReleaseError) Error() string {
	return fmt.Sprintf("workspace still owns %s after down", e.held.Summary())
}

func (e *incompleteReleaseError) Help() string {
	return "inspect the resources, remove them, then retry trocker release"
}
func (e *incompleteReleaseError) Note() string { return "registration retained" }
