package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestDiscoverWorktreesAndMoves(t *testing.T) {
	isolateGit(t)
	directory := t.TempDir()
	main := initRepository(t, filepath.Join(directory, "main checkout"))
	linked := filepath.Join(directory, "linked checkout")
	git(t, main, "worktree", "add", "-b", "feature", linked)
	nested := filepath.Join(linked, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := workspace.Discover(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := workspace.Discover(t.Context(), nested)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Root != canonical(t, linked) || checkout.CommonDir != original.CommonDir || checkout.GitDir == original.GitDir {
		t.Fatalf("worktree association incorrect: main=%+v linked=%+v", original, checkout)
	}
	if original.Root != canonical(t, main) || original.GitDir != original.CommonDir {
		t.Fatalf("unexpected main checkout: %+v", original)
	}
	git(t, linked, "switch", "-c", "another-branch")
	afterBranch, err := workspace.Discover(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if afterBranch != checkout {
		t.Fatal("branch switch changed checkout association")
	}
	moved := filepath.Join(directory, "moved checkout")
	git(t, main, "worktree", "move", linked, moved)
	afterMove, err := workspace.Discover(t.Context(), moved)
	if err != nil {
		t.Fatal(err)
	}
	if afterMove.Root != canonical(t, moved) || afterMove.GitDir != checkout.GitDir || afterMove.CommonDir != checkout.CommonDir {
		t.Fatalf("move lost Git association: %+v", afterMove)
	}
	clone := filepath.Join(directory, "independent clone")
	git(t, directory, "clone", "--local", main, clone)
	independent, err := workspace.Discover(t.Context(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if independent.GitDir == original.GitDir || independent.CommonDir == original.CommonDir {
		t.Fatal("independent clone shares workspace association")
	}
}

func TestListPreservesLockedAndStaleWorktrees(t *testing.T) {
	isolateGit(t)
	directory := t.TempDir()
	main := initRepository(t, filepath.Join(directory, "main"))
	locked := filepath.Join(directory, "locked checkout")
	stale := filepath.Join(directory, "stale checkout")
	git(t, main, "worktree", "add", "--detach", locked)
	git(t, main, "worktree", "add", "--detach", stale)
	git(t, main, "worktree", "lock", "--reason", "keep this\nworktree", locked)
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}
	before := git(t, main, "worktree", "list", "--porcelain", "-z")
	records, err := workspace.List(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]workspace.Worktree{}
	for _, record := range records {
		paths[record.Path] = record
	}
	if len(paths) != 3 || !paths[canonical(t, locked)].Locked || !paths[filepath.Join(canonical(t, directory), "stale checkout")].Prunable {
		t.Fatalf("lost locked/stale records: %+v", records)
	}
	if after := git(t, main, "worktree", "list", "--porcelain", "-z"); after != before {
		t.Fatal("listing changed worktree metadata")
	}
}

func TestDiscoveryIgnoresInheritedRepositorySelection(t *testing.T) {
	isolateGit(t)
	directory := t.TempDir()
	selected := initRepository(t, filepath.Join(directory, "selected"))
	other := initRepository(t, filepath.Join(directory, "other"))
	want, err := workspace.Discover(t.Context(), selected)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, "unrelated-index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
	t.Setenv("GIT_CONFIG_VALUE_0", other)
	got, err := workspace.Discover(t.Context(), selected)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("inherited environment redirected discovery: %+v", got)
	}
	records, err := workspace.List(t.Context(), selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Path != want.Root {
		t.Fatalf("inherited environment redirected listing: %+v", records)
	}
	if os.Getenv("GIT_DIR") != filepath.Join(other, ".git") {
		t.Fatal("discovery changed process environment")
	}
}

func TestDiscoverPreservesUnusualPathsAndSymlinks(t *testing.T) {
	isolateGit(t)
	directory := t.TempDir()
	name := "checkout with spaces"
	if runtime.GOOS != "windows" {
		name = "checkout\nwith\ttabs and trailing space "
	}
	main := initRepository(t, filepath.Join(directory, name))
	checkout, err := workspace.Discover(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Root != canonical(t, main) {
		t.Fatalf("path was trimmed or split: %q", checkout.Root)
	}
	records, err := workspace.List(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Path != checkout.Root {
		t.Fatalf("porcelain path was split: %+v", records)
	}
	if runtime.GOOS == "windows" {
		return
	} // Creating symlinks can require privileges on Windows.
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(main, alias); err != nil {
		t.Fatal(err)
	}
	viaAlias, err := workspace.Discover(t.Context(), alias)
	if err != nil {
		t.Fatal(err)
	}
	if viaAlias != checkout {
		t.Fatal("symlink created a second checkout association")
	}
}

func TestDiscoveryIsReadOnly(t *testing.T) {
	isolateGit(t)
	main := initRepository(t, filepath.Join(t.TempDir(), "main"))
	path := filepath.Join(main, "untracked.txt")
	if err := os.WriteFile(path, []byte("keep this data"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := git(t, main, "status", "--porcelain=v1", "-z")
	before, err := os.ReadDir(main)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Discover(t.Context(), main); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.List(t.Context(), main); err != nil {
		t.Fatal(err)
	}
	if after := git(t, main, "status", "--porcelain=v1", "-z"); after != status {
		t.Fatal("discovery changed checkout state")
	}
	after, err := os.ReadDir(main)
	if err != nil {
		t.Fatal(err)
	}
	names := func(entries []os.DirEntry) []string {
		var result []string
		for _, entry := range entries {
			result = append(result, entry.Name())
		}
		return result
	}
	if !reflect.DeepEqual(names(before), names(after)) {
		t.Fatal("discovery created workspace files")
	}
	content, err := os.ReadFile(path) //nolint:gosec // File is owned by this temporary test repository.
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep this data" {
		t.Fatal("discovery modified application data")
	}
}

func TestDiscoverRepositoryWithoutCommits(t *testing.T) {
	isolateGit(t)
	directory := t.TempDir()
	git(t, directory, "init", "--initial-branch=main")
	checkout, err := workspace.Discover(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Root != canonical(t, directory) || checkout.GitDir != checkout.CommonDir {
		t.Fatalf("empty repository not recognized: %+v", checkout)
	}
}

func TestDiscoverSeparateGitDirectory(t *testing.T) {
	isolateGit(t)
	directory := t.TempDir()
	root := filepath.Join(directory, "checkout")
	admin := filepath.Join(directory, "git metadata")
	git(t, directory, "init", "--separate-git-dir", admin, root)
	checkout, err := workspace.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Root != canonical(t, root) || checkout.GitDir != canonical(t, admin) || checkout.CommonDir != canonical(t, admin) {
		t.Fatalf("separate Git directory was misidentified: %+v", checkout)
	}
}

func TestDiscoverRelativeLinkedWorktree(t *testing.T) {
	isolateGit(t)
	directory := t.TempDir()
	main := initRepository(t, filepath.Join(directory, "main"))
	name := "linked with spaces"
	if runtime.GOOS != "windows" {
		name = "linked\nwith a trailing newline\n"
	}
	linked := filepath.Join(directory, name)
	// Git versions predating relative worktree links ignore this config setting;
	// newer versions exercise the relative .git/commondir layout.
	git(t, main, "-c", "worktree.useRelativePaths=true", "worktree", "add", "--detach", linked)
	checkout, err := workspace.Discover(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	original, err := workspace.Discover(t.Context(), main)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Root != canonical(t, linked) || checkout.CommonDir != original.CommonDir || checkout.GitDir == original.GitDir {
		t.Fatalf("linked checkout association incorrect: %+v", checkout)
	}
	records, err := workspace.List(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record.Path == checkout.Root {
			found = true
		}
	}
	if len(records) != 2 || !found {
		t.Fatalf("linked path lost in listing: %+v", records)
	}
}

func TestDiscoveryFailures(t *testing.T) {
	isolateGit(t)
	directory := t.TempDir()
	if checkout, err := workspace.Discover(t.Context(), directory); err == nil || checkout != (workspace.Checkout{}) {
		t.Fatalf("nonrepository result = %+v, %v", checkout, err)
	}
	bare := filepath.Join(directory, "bare.git")
	git(t, directory, "init", "--bare", bare)
	if checkout, err := workspace.Discover(t.Context(), bare); err == nil || checkout != (workspace.Checkout{}) {
		t.Fatalf("bare repository reported as checkout: %+v, %v", checkout, err)
	}
	records, err := workspace.List(t.Context(), bare)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !records[0].Bare {
		t.Fatalf("bare worktree record lost: %+v", records)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := workspace.Discover(ctx, directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if _, err := workspace.List(ctx, directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := workspace.Discover(t.Context(), directory); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing Git = %v", err)
	}
}

func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("workspace tests require Git installed: ", err)
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
}

func initRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, path, "init", "--initial-branch=main")
	git(t, path, "-c", "user.name=Trocker Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
	return path
}

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // Arguments are test-controlled and target disposable repositories.
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func canonical(t *testing.T, path string) string {
	t.Helper()
	result, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
