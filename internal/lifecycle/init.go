// Package lifecycle coordinates configuration, identity, and SDK workflows.
package lifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v4"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/generated"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type InitProposal struct {
	Checkout   workspace.Checkout
	TargetID   string
	TargetHost string
	ExistingID string
	Moved      bool
	NewTarget  bool
	// Manifest is populated only for a newly proposed, secret-free manifest.
	Manifest []byte
	// Digest identifies the decisions this proposal asks the user to approve.
	Digest string
}

var ErrDeclined = errors.New("initialization not confirmed; nothing written")

// initDigestVersion invalidates digests captured against an older proposal shape.
const initDigestVersion = "trocker-init-1"

// initDigest covers what a registration approval actually decides: the effective
// manifest, the Docker target, and whether a moved checkout is relinked. The
// checkout path and the workspace identity are deliberately excluded, so an
// approval of a configuration applies to every worktree that uses it. Editing
// the manifest or switching daemons changes the digest and needs a new approval.
func initDigest(manifest []byte, creating bool, targetID, targetHost string, moved bool) string {
	var canonical strings.Builder
	write := func(value string) {
		canonical.WriteString(strconv.Itoa(len(value)))
		canonical.WriteByte(':')
		canonical.WriteString(value)
	}
	write(initDigestVersion)
	if creating {
		write("create")
	} else {
		write("keep")
	}
	write(string(manifest))
	write(targetID)
	write(targetHost)
	if moved {
		write("moved")
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

// PrepareInit returns the proposal that init would confirm, without writing
// configuration or registration state.
func PrepareInit(ctx context.Context, directory string, store state.Store) (InitProposal, error) {
	prepared, err := prepareInit(ctx, directory, store)
	if err != nil {
		return InitProposal{}, err
	}
	return prepared.proposal, nil
}

// Init validates and presents the proposal before creating any local state.
// It never creates or adopts Docker resources.
func Init(ctx context.Context, directory string, store state.Store, confirm func(InitProposal) (bool, error)) (workspace.Registration, error) {
	prepared, err := prepareInit(ctx, directory, store)
	if err != nil {
		return workspace.Registration{}, err
	}
	accepted, err := confirm(prepared.proposal)
	if err != nil {
		return workspace.Registration{}, err
	}
	if !accepted {
		return workspace.Registration{}, ErrDeclined
	}
	return applyInit(ctx, store, prepared)
}

type preparedInit struct {
	proposal         InitProposal
	originalManifest []byte
}

func prepareInit(ctx context.Context, directory string, store state.Store) (prepared preparedInit, err error) {
	checkout, err := workspace.Discover(ctx, directory)
	if err != nil {
		return prepared, err
	}
	original, proposed, err := prepareInitManifest(ctx, checkout.Root)
	if err != nil {
		return prepared, err
	}
	registry, err := store.Read()
	if err != nil {
		return prepared, err
	}
	index, moved, err := workspace.Find(ctx, checkout, registry.Workspaces)
	if err != nil {
		return prepared, err
	}
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		return prepared, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()

	effective, creating := original, false
	if len(proposed) > 0 {
		effective, creating = proposed, true
	}
	proposal := InitProposal{
		Checkout: checkout, TargetID: target.ID, TargetHost: target.Host,
		Moved: moved, NewTarget: true, Manifest: proposed,
		Digest: initDigest(effective, creating, target.ID, target.Host, moved),
	}
	if index >= 0 {
		existing := registry.Workspaces[index]
		proposal.ExistingID = existing.ID
		proposal.NewTarget = !slices.Contains(existing.Targets, target.ID)
	}
	return preparedInit{proposal: proposal, originalManifest: original}, nil
}

// A non-nil proposal means the manifest was absent. Existing configuration is
// validated and retained verbatim for the post-confirmation comparison.
func prepareInitManifest(ctx context.Context, root string) (original, proposed []byte, err error) {
	path := filepath.Join(root, "trocker.yaml")
	original, err = os.ReadFile(path) //nolint:gosec // Manifest path is within the discovered checkout.
	if err == nil {
		manifest, _, err := compose.Load(ctx, path)
		if err == nil {
			_, err = generated.Validate(ctx, manifest)
		}
		return original, nil, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}

	project, err := compose.DiscoverFiles(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	manifest := config.Manifest{Version: 1}
	for _, file := range project.ComposeFiles {
		relative, err := filepath.Rel(root, file)
		if err != nil {
			relative = file
		}
		manifest.Compose = append(manifest.Compose, relative)
	}
	if err := manifest.Validate(); err != nil {
		return nil, nil, err
	}
	proposed, err = yaml.Dump(manifest)
	return nil, proposed, err
}

func applyInit(ctx context.Context, store state.Store, prepared preparedInit) (registration workspace.Registration, err error) {
	if err := ctx.Err(); err != nil {
		return registration, err
	}
	proposal := prepared.proposal
	lock, err := store.LockWorkspace(ctx, proposal.Checkout.GitDir)
	if err != nil {
		return registration, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()

	if err := revalidateInit(ctx, store, proposal); err != nil {
		return registration, err
	}
	// Sweeping other workspaces is maintenance of provably inert registrations,
	// which the confirmation does not describe, so it happens only after consent
	// and never on a declined init.
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		return registration, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	if err := reapDeadWorkspaces(ctx, store, target); err != nil {
		return registration, err
	}
	if err := applyInitManifest(prepared); err != nil {
		return registration, err
	}
	return registerLocked(ctx, store, proposal.Checkout, proposal.TargetID)
}

// Recheck under the workspace lock before any manifest/registry writes. Prompts
// and Docker calls happen before locking; registerLocked takes the registry lock last.
func revalidateInit(ctx context.Context, store state.Store, proposal InitProposal) error {
	current, err := workspace.Discover(ctx, proposal.Checkout.Root)
	if err != nil {
		return err
	}
	if current != proposal.Checkout {
		return fmt.Errorf("checkout changed during confirmation; retry init")
	}
	registry, err := store.Read()
	if err != nil {
		return err
	}
	index, _, err := workspace.Find(ctx, current, registry.Workspaces)
	if err != nil {
		return err
	}
	if proposal.ExistingID != "" && (index < 0 || registry.Workspaces[index].ID != proposal.ExistingID) {
		return fmt.Errorf("registration changed during confirmation; explicit recovery required")
	}
	return nil
}

func applyInitManifest(prepared preparedInit) error {
	path := filepath.Join(prepared.proposal.Checkout.Root, "trocker.yaml")
	if prepared.proposal.Manifest != nil {
		return createFile(path, prepared.proposal.Manifest)
	}
	latest, err := os.ReadFile(path) //nolint:gosec // Recheck the same selected manifest after confirmation.
	if err != nil {
		return err
	}
	if !bytes.Equal(prepared.originalManifest, latest) {
		return fmt.Errorf("manifest changed during confirmation; retry init")
	}
	return nil
}

// Register persists an explicitly approved association under workspace and registry locks.
func Register(ctx context.Context, store state.Store, checkout workspace.Checkout, targetID string) (registration workspace.Registration, err error) {
	lock, err := store.LockWorkspace(ctx, checkout.GitDir)
	if err != nil {
		return registration, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return registerLocked(ctx, store, checkout, targetID)
}

func registerLocked(ctx context.Context, store state.Store, checkout workspace.Checkout, targetID string) (registration workspace.Registration, err error) {
	err = store.Update(ctx, func(registry *state.Registry) error {
		index, _, err := workspace.Find(ctx, checkout, registry.Workspaces)
		if err != nil {
			return err
		}
		if index < 0 {
			id, err := uuid.NewRandom()
			if err != nil {
				return err
			}
			registry.Workspaces = append(registry.Workspaces, workspace.Registration{ID: id.String(), Checkout: checkout, Targets: []string{targetID}})
			index = len(registry.Workspaces) - 1
		} else {
			registry.Workspaces[index].Checkout = checkout
			if !slices.Contains(registry.Workspaces[index].Targets, targetID) {
				registry.Workspaces[index].Targets = append(registry.Workspaces[index].Targets, targetID)
			}
		}
		registration = registry.Workspaces[index]
		return nil
	})
	if err != nil {
		return workspace.Registration{}, err
	}
	// Commit the registry first. If publishing the marker fails, a retry uses
	// that existing UUID rather than allocating a replacement.
	marker, err := workspace.Identity(checkout)
	if err != nil {
		return workspace.Registration{}, err
	}
	if marker != "" && marker != registration.ID {
		return workspace.Registration{}, fmt.Errorf("workspace marker changed during registration; explicit recovery required")
	}
	if marker == "" {
		if err := createFile(filepath.Join(checkout.GitDir, workspace.IdentityFile), []byte(registration.ID+"\n")); err != nil {
			return workspace.Registration{}, err
		}
	}
	return registration, nil
}

func createFile(path string, content []byte) (err error) {
	// A same-directory hard link publishes the complete file without replacing a
	// file created by another process. Unlike rename, Link fails if it exists.
	file, err := os.CreateTemp(filepath.Dir(path), ".trocker-init-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(file.Name())) }()
	if _, err := file.Write(content); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil {
		return fmt.Errorf("cannot publish file without overwriting; retry init after checking the destination: %w", err)
	}
	return nil
}
