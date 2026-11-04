package state

import (
	"errors"
	"os"
	"testing"
)

func TestGenerationIndexRenameFailureRetainsCompleteFiles(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	const generation = "e353936d-8f28-4b8e-a7a2-a629702886bd"
	if err := root.Mkdir(generation, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(generation+"/app.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir("current.json", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("current.json/keep", []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeGenerationIndex(t.Context(), root, generation); err == nil {
		t.Fatal("rename over a directory succeeded")
	}
	if data, err := root.ReadFile("current.json/keep"); err != nil || string(data) != "untouched" {
		t.Fatal("failed rename damaged existing state", err)
	}
	if _, err := root.Stat(generation + "/app.json"); err != nil {
		t.Fatal("complete generation removed", err)
	}
	if _, err := root.Stat(".current-" + generation + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed index staging retained", err)
	}
}
