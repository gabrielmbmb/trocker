package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

// init publishes a generated trocker.yaml only where none exists, so a manifest
// that appeared after init looked is kept rather than replaced.
func TestCreateFileKeepsAnExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "trocker.yaml")
	if err := createFile(path, []byte("generated\n")); err != nil {
		t.Fatal(err)
	}
	if err := createFile(path, []byte("replacement\n")); err == nil {
		t.Fatal("createFile replaced an existing file")
	}
	content, err := os.ReadFile(path) //nolint:gosec // Test-owned file.
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "generated\n" {
		t.Fatalf("content = %q", content)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v %v", entries, err)
	}
}
