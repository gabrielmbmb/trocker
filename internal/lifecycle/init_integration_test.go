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

// One approval covers every worktree that shares the approved configuration:
// the digest deliberately excludes the checkout path and the identity.
func TestInitDigestApprovalCoversAnotherWorktree(t *testing.T) {
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	if err := os.WriteFile(filepath.Join(main, "compose.yaml"), []byte("services:\n  api:\n    image: busybox\n    expose: ['8000']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "trocker.yaml"), []byte("version: 1\ncompose: [compose.yaml]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", "compose.yaml", "trocker.yaml")
	git(t, main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "manifest")
	other := filepath.Join(directory, "other")
	git(t, main, "worktree", "add", "--detach", other, "HEAD")

	approved, err := lifecycle.PrepareInit(t.Context(), main, store)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Digest == "" {
		t.Fatal("proposal carried no digest")
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("prepare must not register: %+v %v", registry, err)
	}

	if _, err := lifecycle.Init(t.Context(), other, store, func(p lifecycle.InitProposal) (bool, error) {
		if p.Digest != approved.Digest {
			t.Fatalf("another worktree produced a different digest: %s != %s", p.Digest, approved.Digest)
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	registry, err = store.Read()
	if err != nil || len(registry.Workspaces) != 1 {
		t.Fatalf("approval did not register the second worktree: %+v %v", registry, err)
	}
	registered, err := filepath.EvalSymlinks(registry.Workspaces[0].Checkout.Root)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(other)
	if err != nil {
		t.Fatal(err)
	}
	if registered != expected {
		t.Fatalf("approval registered %s, want %s", registered, expected)
	}

	// Editing the approved configuration must invalidate the same digest.
	third := filepath.Join(directory, "third")
	git(t, main, "worktree", "add", "--detach", third, "HEAD")
	if err := os.WriteFile(filepath.Join(third, "trocker.yaml"), []byte("version: 1\ncompose: [compose.yaml]\nread_write_paths: [.]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Init(t.Context(), third, store, func(p lifecycle.InitProposal) (bool, error) {
		return p.Digest == approved.Digest, nil
	}); !errors.Is(err, lifecycle.ErrDeclined) {
		t.Fatalf("edited manifest must not match the approved digest: %v", err)
	}
	registry, err = store.Read()
	if err != nil || len(registry.Workspaces) != 1 {
		t.Fatalf("declined registration was written: %+v %v", registry, err)
	}
}

// Compose is not validated before an unapproved environment wrapper has run,
// so registering in that state would skip validation the user never saw.
func TestInitDoesNotRegisterBeforeTheWrapperRuns(t *testing.T) {
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
	proposal, err := lifecycle.PrepareInit(t.Context(), main, store)
	if err != nil || !proposal.ComposeDeferred || proposal.WrapperApproved {
		t.Fatalf("proposal = %+v, %v", proposal, err)
	}
	if _, err := lifecycle.Init(t.Context(), main, store, func(lifecycle.InitProposal) (bool, error) { return true, nil }); err == nil {
		t.Fatal("init registered a workspace whose Compose files were never validated")
	}
	assertAbsent(t, store.Directory)
}
