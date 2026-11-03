package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestCheckoutLiveness(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		root     bool
		gitDir   bool
		liveness workspace.Liveness
	}{
		{name: "live", root: true, gitDir: true, liveness: workspace.CheckoutLive},
		{name: "gone", liveness: workspace.CheckoutGone},
		{name: "git metadata survives", gitDir: true, liveness: workspace.CheckoutUnconfirmed},
		{name: "root alone is live", root: true, liveness: workspace.CheckoutLive},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			root, gitDir := filepath.Join(directory, "worktree"), filepath.Join(directory, "repo", ".git", "worktrees", "name")
			for path, present := range map[string]bool{root: test.root, gitDir: test.gitDir} {
				if !present {
					continue
				}
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			liveness, err := workspace.CheckoutLiveness(workspace.Checkout{Root: root, GitDir: gitDir})
			if err != nil {
				t.Fatal(err)
			}
			if liveness != test.liveness {
				t.Fatalf("liveness = %v, want %v", liveness, test.liveness)
			}
		})
	}
}
