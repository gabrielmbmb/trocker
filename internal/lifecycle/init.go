// Package lifecycle coordinates configuration, identity, and SDK workflows.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v4"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/generated"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

// InitProposal describes what init changes for a checkout.
type InitProposal struct {
	Checkout   workspace.Checkout
	TargetID   string
	TargetHost string
	ExistingID string
	Moved      bool
	NewTarget  bool
	// Manifest is populated only for a newly proposed, secret-free manifest.
	Manifest []byte
	// EnvironmentWrapper is the declared environment_wrapper command.
	EnvironmentWrapper []string
}

// PrepareInit returns what init would change, without writing configuration or
// registration state.
func PrepareInit(ctx context.Context, directory string, store state.Store) (proposal InitProposal, err error) {
	checkout, err := workspace.Discover(ctx, directory)
	if err != nil {
		return proposal, err
	}
	proposed, wrapper, err := prepareInitManifest(ctx, checkout.Root)
	if err != nil {
		return proposal, err
	}
	registry, err := store.Read()
	if err != nil {
		return proposal, err
	}
	index, moved, err := workspace.Find(ctx, checkout, registry.Workspaces)
	if err != nil {
		return proposal, err
	}
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		return proposal, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()

	proposal = InitProposal{
		Checkout: checkout, TargetID: target.ID, TargetHost: target.Host,
		Moved: moved, NewTarget: true, Manifest: proposed, EnvironmentWrapper: wrapper,
	}
	if index >= 0 {
		existing := registry.Workspaces[index]
		proposal.ExistingID = existing.ID
		proposal.NewTarget = !slices.Contains(existing.Targets, target.ID)
	}
	return proposal, nil
}

// Init validates the checkout's configuration and registers it. It never
// creates or adopts Docker resources, and invalid configuration leaves no state.
func Init(ctx context.Context, directory string, store state.Store) (registration workspace.Registration, err error) {
	proposal, err := PrepareInit(ctx, directory, store)
	if err != nil {
		return registration, err
	}
	lock, err := store.LockWorkspace(ctx, proposal.Checkout.GitDir)
	if err != nil {
		return registration, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return applyInit(ctx, store, proposal)
}

// A non-nil proposal means the manifest was absent. An existing manifest is
// validated and kept unchanged.
func prepareInitManifest(ctx context.Context, root string) (proposed []byte, wrapper []string, err error) {
	path := filepath.Join(root, "trocker.yaml")
	_, err = os.Stat(path)
	if err == nil {
		manifest, err := validateInitManifest(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		return nil, manifest.EnvironmentWrapper, nil
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
	return proposed, nil, err
}

// validateInitManifest checks an existing manifest. Compose interpolates with
// this process's environment, so a declared environment wrapper must already
// be running, as the CLI arranges before init.
func validateInitManifest(ctx context.Context, path string) (*config.Manifest, error) {
	manifest, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if _, _, err := compose.Load(ctx, path, nil); err != nil {
		return nil, err
	}
	if err := checkStablePortRange(manifest, ephemeralPortRange()); err != nil {
		return nil, err
	}
	_, err = generated.Validate(ctx, manifest)
	return manifest, err
}

// applyInit writes the proposed manifest and the registration. The caller holds
// the workspace lock; registerLocked takes the registry lock last and looks the
// checkout up again under it, so a registration made since the proposal is
// reused rather than duplicated.
func applyInit(ctx context.Context, store state.Store, proposal InitProposal) (registration workspace.Registration, err error) {
	if err := ctx.Err(); err != nil {
		return registration, err
	}
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		return registration, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	if err := reapDeadWorkspaces(ctx, store, target); err != nil {
		return registration, err
	}
	path := filepath.Join(proposal.Checkout.Root, "trocker.yaml")
	if proposal.Manifest != nil {
		if err := createFile(path, proposal.Manifest); err != nil {
			return registration, err
		}
	}
	manifest, err := config.Load(path)
	if err != nil {
		return registration, err
	}
	registration, err = registerLocked(ctx, store, proposal.Checkout, proposal.TargetID)
	if err != nil {
		return registration, err
	}
	// Stable assignments are metadata, not containers. Creating them here makes
	// the values available before the first startup and gives external consumers
	// a chance to be configured first.
	if manifest.StablePorts {
		planned, err := preparePlan(ctx, proposal.Checkout.Root, store, nil, nil)
		if err != nil {
			return registration, err
		}
		if _, err := ensureStableAssignments(ctx, store, planned, target); err != nil {
			return registration, err
		}
	}
	return registration, nil
}

// Register persists a checkout's association with a target under workspace and
// registry locks.
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
