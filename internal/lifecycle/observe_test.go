package lifecycle_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestUnregisteredObservationsWriteNothing(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	if _, err := lifecycle.Observe(t.Context(), main, store, nil); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("status: %v", err)
	}
	if _, err := lifecycle.Endpoints(t.Context(), main, store, nil); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("endpoints: %v", err)
	}
	if _, err := os.Stat(store.Directory); !os.IsNotExist(err) {
		t.Fatalf("read-only query created state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(main, "trocker.yaml")); !os.IsNotExist(err) {
		t.Fatalf("read-only query created configuration: %v", err)
	}
}

func TestRegistrationSurvivesBranchAndLinkedWorktreeMove(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	checkout, err := workspace.Discover(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	first, err := lifecycle.Register(t.Context(), store, checkout, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	git(t, main, "switch", "-c", "new-branch")
	checkout, err = workspace.Discover(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	same, err := lifecycle.Register(t.Context(), store, checkout, "daemon")
	if err != nil || same.ID != first.ID {
		t.Fatalf("branch switch changed identity: %v", err)
	}
	linked := filepath.Join(directory, "linked")
	git(t, main, "worktree", "add", "--detach", linked)
	linkedCheckout, err := workspace.Discover(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Register(t.Context(), store, linkedCheckout, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("linked worktree reused main identity")
	}
	moved := filepath.Join(directory, "moved")
	git(t, main, "worktree", "move", linked, moved)
	movedCheckout, err := workspace.Discover(t.Context(), moved)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	index, relocated, err := workspace.Find(t.Context(), movedCheckout, registry.Workspaces)
	if err != nil || !relocated || registry.Workspaces[index].ID != second.ID {
		t.Fatalf("move not recognized: %v", err)
	}
	stillOld, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if stillOld.Workspaces[index].Checkout.Root != linkedCheckout.Root {
		t.Fatal("read-only lookup persisted move")
	}
	preserved, err := lifecycle.Register(t.Context(), store, movedCheckout, "daemon")
	if err != nil || preserved.ID != second.ID || preserved.Checkout.Root != movedCheckout.Root {
		t.Fatalf("move lost registration: %+v, %v", preserved, err)
	}
	// Reappearing at the old path makes a move ambiguous, not an identity reset.
	next := filepath.Join(directory, "next")
	git(t, main, "worktree", "move", moved, next)
	if err := os.Mkdir(moved, 0o700); err != nil {
		t.Fatal(err)
	}
	nextCheckout, err := workspace.Discover(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Register(t.Context(), store, nextCheckout, "daemon"); err == nil {
		t.Fatal("ambiguous move accepted")
	}
}

func TestConflictingCheckoutDoesNotResetIdentity(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	checkout, err := workspace.Discover(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	first, err := lifecycle.Register(t.Context(), store, checkout, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	conflicting := checkout
	conflicting.GitDir = filepath.Join(directory, "other.git")
	if _, err := lifecycle.Register(t.Context(), store, conflicting, "daemon"); err == nil {
		t.Fatal("conflicting association accepted")
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].ID != first.ID {
		t.Fatalf("identity lost: %+v %v", registry, err)
	}
}

func TestLostRegistryAndMovedMainRequireRecovery(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	checkout, err := workspace.Discover(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	first, err := lifecycle.Register(t.Context(), store, checkout, "daemon")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Register(t.Context(), state.Store{Directory: filepath.Join(directory, "lost-state")}, checkout, "daemon"); err == nil {
		t.Fatal("missing registry silently replaced an existing identity")
	}
	moved := filepath.Join(directory, "moved-main")
	if err := os.Rename(main, moved); err != nil {
		t.Fatal(err)
	}
	current, err := workspace.Discover(t.Context(), moved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Register(t.Context(), store, current, "daemon"); err == nil {
		t.Fatal("moving all Git metadata silently replaced identity")
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].ID != first.ID {
		t.Fatal("failed recovery changed registration")
	}
}

func repository(t *testing.T) (string, string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	directory := t.TempDir()
	main := filepath.Join(directory, "checkout")
	if err := os.Mkdir(main, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "--initial-branch=main")
	git(t, main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
	return directory, main
}

func git(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // Fixed test operations on temporary repositories.
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture Git: %v: %s", err, output)
	}
}
