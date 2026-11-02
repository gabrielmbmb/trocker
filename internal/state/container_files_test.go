package state_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/state"
)

func TestContainerGenerationPrivacyAndScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("container file delivery requires POSIX host permissions")
	}
	store := state.Store{Directory: filepath.Join(t.TempDir(), "private state")}
	files := map[string][]byte{"app.json": []byte("{}"), "empty.txt": nil}
	native, err := store.PublishGenerated(t.Context(), generatedWorkspace, "daemon", "app", files)
	if err != nil {
		t.Fatal(err)
	}
	container, err := store.PublishContainerFiles(t.Context(), generatedWorkspace, "daemon", "app", files)
	if err != nil {
		t.Fatal(err)
	}
	if native["app.json"] == container["app.json"] {
		t.Fatal("native and container scopes overlap")
	}
	for _, path := range container {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o444 {
			t.Fatal("container file permissions", err)
		}
		for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
			info, err := os.Stat(directory)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("publicly traversable generated tree", err)
			}
			if directory == store.Directory {
				break
			}
		}
	}
	repeated, err := store.PublishContainerFiles(t.Context(), generatedWorkspace, "daemon", "app", files)
	if err != nil || repeated["app.json"] != container["app.json"] {
		t.Fatal("unchanged container generation not reused", err)
	}
	if err := os.Chmod(container["app.json"], 0o644); err != nil { //nolint:gosec // Insecure mutation must be rejected.
		t.Fatal(err)
	}
	if _, err := store.PublishContainerFiles(t.Context(), generatedWorkspace, "daemon", "app", files); err == nil {
		t.Fatal("writable cached container file accepted")
	}
	if _, err := store.PublishGenerated(t.Context(), generatedWorkspace, "daemon", "app", files); err != nil {
		t.Fatal("container corruption affected native scope", err)
	}
}
