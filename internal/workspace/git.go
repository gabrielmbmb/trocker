// Package workspace identifies Git checkouts without changing repositories.
package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Checkout paths locate a workspace; none is its persistent Trocker identity.
// Linked worktrees share CommonDir but have distinct Root and GitDir paths.
type Checkout struct {
	Root      string
	GitDir    string
	CommonDir string
}

type Worktree struct {
	Path     string
	Bare     bool
	Locked   bool
	Prunable bool
}

// Discover identifies the checkout containing directory, including from a
// subdirectory. Bare repositories and directories outside a checkout fail.
func Discover(ctx context.Context, directory string) (Checkout, error) {
	root, err := gitPath(ctx, directory, "--show-toplevel")
	if err != nil {
		return Checkout{}, err
	}
	gitDir, err := gitPath(ctx, directory, "--absolute-git-dir")
	if err != nil {
		return Checkout{}, err
	}
	commonDir, err := gitPath(ctx, directory, "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Checkout{}, err
	}
	return Checkout{Root: root, GitDir: gitDir, CommonDir: commonDir}, nil
}

// List returns Git's worktree records, including bare, locked and stale entries.
// It does not prune, repair, register, or inspect the listed checkouts.
func List(ctx context.Context, directory string) ([]Worktree, error) {
	output, err := runGit(ctx, directory, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(output)
}

func gitPath(ctx context.Context, directory string, args ...string) (string, error) {
	output, err := runGit(ctx, directory, append([]string{"rev-parse"}, args...)...)
	if err != nil {
		return "", err
	}
	// Strip only the record terminator; whitespace and even newlines can belong
	// to a Unix path. Request each path separately to avoid ambiguous splitting.
	path := strings.TrimSuffix(string(output), "\n")
	if runtime.GOOS == "windows" {
		path = strings.TrimSuffix(path, "\r")
	}
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("git rev-parse: expected an absolute path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve Git path: %w", err)
	}
	return canonical, nil
}

func runGit(ctx context.Context, directory string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "git", args...) //nolint:gosec // Private callers supply fixed read-only Git commands; no shell is used.
	command.Dir = directory
	command.Env = discoveryEnvironment(command.Environ())
	output, err := command.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		// Do not echo raw stderr: Git configuration can contain sensitive values.
		return nil, fmt.Errorf("git %s failed in the selected directory (Git must be installed and the repository accessible): %w", args[0], err)
	}
	return output, nil
}

func discoveryEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		// Repository-local variables listed by `git rev-parse --local-env-vars`
		// must not redirect directory-based discovery (e.g. when run from a hook).
		// Preserve normal system/global configuration, including safe.directory.
		switch strings.ToUpper(name) {
		case "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
			"GIT_OBJECT_DIRECTORY", "GIT_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE",
			"GIT_GRAFT_FILE", "GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE",
			"GIT_PREFIX", "GIT_SHALLOW_FILE", "GIT_COMMON_DIR":
			continue
		}
		result = append(result, entry)
	}
	return result
}

func parseWorktrees(output []byte) ([]Worktree, error) {
	var worktrees []Worktree
	var current *Worktree
	for field := range strings.SplitSeq(string(output), "\x00") {
		if len(field) == 0 {
			current = nil
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		if key == "worktree" {
			if current != nil || !filepath.IsAbs(value) {
				return nil, fmt.Errorf("git worktree list: invalid worktree record")
			}
			worktrees = append(worktrees, Worktree{Path: filepath.Clean(value)})
			current = &worktrees[len(worktrees)-1]
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("git worktree list: attribute without a worktree")
		}
		switch key {
		case "bare":
			current.Bare = true
		case "locked":
			current.Locked = true
		case "prunable":
			current.Prunable = true
		}
	}
	if len(worktrees) == 0 {
		return nil, fmt.Errorf("git worktree list: no worktree records")
	}
	return worktrees, nil
}
