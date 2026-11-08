// Package state persists identity metadata under cross-process locks.
package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/moby/sys/atomicwriter"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

type Registry struct {
	Version     int                      `json:"version"`
	Workspaces  []workspace.Registration `json:"workspaces"`
	Ports       []PortPreference         `json:"port_preferences,omitempty"`
	Assignments []PortAssignment         `json:"port_assignments,omitempty"`
}

type Store struct{ Directory string }

func DefaultDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "trocker"), nil
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if !filepath.IsAbs(base) {
			return "", fmt.Errorf("LOCALAPPDATA must be an absolute path")
		}
		return filepath.Join(base, "Trocker"), nil
	default:
		base := os.Getenv("XDG_STATE_HOME")
		if !filepath.IsAbs(base) {
			base = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(base, "trocker"), nil
	}
}

// Read is observational: an absent registry is empty, without creating files.
// Only explicit initialization may turn that absence into a new registration.
func (s Store) Read() (Registry, error) {
	if err := privatePath(s.Directory, true); err != nil {
		return Registry{}, err
	}
	path := filepath.Join(s.Directory, "registry.json")
	if err := privatePath(path, false); err != nil {
		return Registry{}, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // The caller selects a private state directory.
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Version: 1}, nil
	}
	if err != nil {
		return Registry{}, fmt.Errorf("read registry: %w", err)
	}
	var registry Registry
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, recoveryError()
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Registry{}, recoveryError()
	}
	if err := registry.validate(); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

// Update holds only the registry lock. Callers must not do network requests,
// prompt, or acquire workspace locks in fn. An error leaves the registry intact.
func (s Store) Update(ctx context.Context, fn func(*Registry) error) (err error) {
	lock, err := s.lock(ctx, "registry")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	registry, err := s.Read()
	if err != nil {
		return err
	}
	before, err := json.Marshal(registry)
	if err != nil {
		return err
	}
	if err := fn(&registry); err != nil {
		return err
	}
	if err := registry.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	after, err := json.Marshal(registry)
	if err != nil {
		return err
	}
	if bytes.Equal(before, after) {
		return nil
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return atomicwriter.WriteFile(filepath.Join(s.Directory, "registry.json"), append(data, '\n'), 0o600)
}

// LockWorkspace serializes initialization before a UUID exists, across targets.
// Lock order is workspace then registry. Never remove lock files while in use.
func (s Store) LockWorkspace(ctx context.Context, gitDir string) (*flock.Flock, error) {
	return s.lock(ctx, fmt.Sprintf("workspace-%x", sha256.Sum256([]byte(gitDir))))
}

func (s Store) lock(ctx context.Context, name string) (*flock.Flock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := s.newLock(name)
	if err != nil {
		return nil, err
	}
	// Short polling applies only to contended OS locks, never Docker readiness.
	if _, err := lock.TryLockContext(ctx, 25*time.Millisecond); err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	return lock, nil
}

func (s Store) newLock(name string) (*flock.Flock, error) {
	if err := privatePath(s.Directory, true); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Directory, 0o700); err != nil {
		return nil, err
	}
	locks := filepath.Join(s.Directory, "locks")
	if err := privatePath(locks, true); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(locks, name+".lock")
	if err := privatePath(path, false); err != nil {
		return nil, err
	}
	return flock.New(path, flock.SetPermissions(0o600)), nil
}

func privatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("state path must be a regular file or directory, not a symlink")
	}
	// Windows uses the containing user profile's inherited ACL, not POSIX modes.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("state permissions are not private; restrict access before retrying")
	}
	return nil
}

func recoveryError() error {
	return fmt.Errorf("registry is corrupt, ambiguous, or unsupported; explicit recovery required (file left unchanged)")
}

func (r Registry) validate() error {
	if r.Version != 1 {
		return recoveryError()
	}
	ids, roots, gitDirs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, registration := range r.Workspaces {
		id, err := uuid.Parse(registration.ID)
		if err != nil || id == uuid.Nil || id.String() != registration.ID || ids[registration.ID] {
			return recoveryError()
		}
		c := registration.Checkout
		if !filepath.IsAbs(c.Root) || !filepath.IsAbs(c.GitDir) || !filepath.IsAbs(c.CommonDir) || roots[c.Root] || gitDirs[c.GitDir] || len(registration.Targets) == 0 {
			return recoveryError()
		}
		targets := map[string]bool{}
		for _, target := range registration.Targets {
			if target == "" || targets[target] {
				return recoveryError()
			}
			targets[target] = true
		}
		ids[registration.ID], roots[c.Root], gitDirs[c.GitDir] = true, true, true
	}
	if err := r.validatePorts(); err != nil {
		return err
	}
	return r.validateAssignments()
}

// RemoveTarget drops one Docker target association together with the port
// preferences that depend on it, and reports whether that removed the
// registration itself. Port preferences must go with their association: a
// preference whose workspace is gone makes the whole registry invalid.
// Callers must hold the registry lock, which Store.Update provides.
func (r *Registry) RemoveTarget(id, targetID string) (removed bool) {
	r.Ports = slices.DeleteFunc(r.Ports, func(preference PortPreference) bool {
		return preference.WorkspaceID == id && preference.TargetID == targetID
	})
	r.Assignments = slices.DeleteFunc(r.Assignments, func(assignment PortAssignment) bool {
		return assignment.WorkspaceID == id && assignment.TargetID == targetID
	})
	index := slices.IndexFunc(r.Workspaces, func(registration workspace.Registration) bool { return registration.ID == id })
	if index < 0 {
		return false
	}
	remaining := slices.DeleteFunc(r.Workspaces[index].Targets, func(target string) bool { return target == targetID })
	if len(remaining) > 0 {
		r.Workspaces[index].Targets = remaining
		return false
	}
	r.Workspaces = slices.Delete(r.Workspaces, index, index+1)
	return true
}
