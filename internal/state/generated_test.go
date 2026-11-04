package state_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/state"
)

const generatedWorkspace = "e353936d-8f28-4b8e-a7a2-a629702886bd"

func TestGeneratedSnapshotsAndIsolation(t *testing.T) {
	store := state.Store{Directory: filepath.Join(t.TempDir(), "private state with spaces")}
	files := map[string][]byte{"app.json": []byte(`{"version":"one"}`), "other.txt": []byte("first")}
	first, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", files)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(first["app.json"])
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal("generated file is not private")
	}
	directory, err := os.Stat(filepath.Dir(first["app.json"]))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && directory.Mode().Perm() != 0o700 {
		t.Fatal("generation directory is not private")
	}
	second, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", files)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.Stat(second["app.json"])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !info.ModTime().Equal(unchanged.ModTime()) {
		t.Fatal("unchanged content was rewritten")
	}
	files["app.json"] = []byte(`{"version":"two"}`)
	changed, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", files)
	if err != nil {
		t.Fatal(err)
	}
	if changed["app.json"] == first["app.json"] {
		t.Fatal("changed content overwrote an existing generation")
	}
	if string(readGenerated(t, first["app.json"])) != `{"version":"one"}` {
		t.Fatal("old snapshot was changed or removed")
	}
	for _, scope := range []struct{ id, target, service string }{
		{generatedWorkspace, "daemon", "app"},
		{generatedWorkspace, "", "App"},
		{"17aa318c-630e-4e56-bca4-452188f698d3", "", "app"},
	} {
		paths, err := store.PublishGenerated(t.Context(), scope.id, scope.target, scope.service, files)
		if err != nil {
			t.Fatal(err)
		}
		if paths["app.json"] == changed["app.json"] {
			t.Fatal("consumer scopes share generated files")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if paths, err := store.PublishGenerated(ctx, generatedWorkspace, "", "app", files); !errors.Is(err, context.Canceled) || paths != nil {
		t.Fatal("canceled publication succeeded", err)
	}
}

func TestGeneratedWriteFailurePreservesPreviousSet(t *testing.T) {
	store := state.Store{Directory: filepath.Join(t.TempDir(), "state")}
	paths, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", map[string][]byte{"a.txt": []byte("original")})
	if err != nil {
		t.Fatal(err)
	}
	consumer := filepath.Dir(filepath.Dir(paths["a.txt"]))
	before := readGenerated(t, filepath.Join(consumer, "current.json"))
	// The first file is written before the filesystem rejects the second filename.
	failed, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", map[string][]byte{"a.txt": []byte("changed"), strings.Repeat("z", 300): []byte("DO_NOT_PRINT_SECRET")})
	if err == nil || failed != nil || strings.Contains(err.Error(), "DO_NOT_PRINT_SECRET") {
		t.Fatal("write failure returned success or leaked values", err)
	}
	if !bytes.Equal(before, readGenerated(t, filepath.Join(consumer, "current.json"))) || string(readGenerated(t, paths["a.txt"])) != "original" {
		t.Fatal("write failure changed previous snapshot")
	}
	entries, err := os.ReadDir(consumer)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatal("failed staging was published or left behind", entries)
	}
}

func TestGeneratedStateRejectsUnsafePathsAndCorruption(t *testing.T) {
	for _, location := range []string{"generated", "generation", "file", "index", "permissions", "corrupt index"} {
		t.Run(location, func(t *testing.T) {
			store := state.Store{Directory: filepath.Join(t.TempDir(), "state")}
			files := map[string][]byte{"app.json": []byte("original")}
			paths, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", files)
			if err != nil {
				t.Fatal(err)
			}
			path := paths["app.json"]
			switch location {
			case "generated":
				path = filepath.Join(store.Directory, "generated")
			case "generation":
				path = filepath.Dir(path)
			case "index", "corrupt index":
				path = filepath.Join(filepath.Dir(filepath.Dir(path)), "current.json")
			}
			outside := t.TempDir()
			switch location {
			case "permissions":
				if runtime.GOOS == "windows" {
					t.Skip("POSIX permission contract")
				}
				if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // Deliberately insecure fixture must be rejected.
					t.Fatal(err)
				}
			case "corrupt index":
				if err := os.WriteFile(path, []byte(`{"version":1,"generation":"../../escape"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Skip("symlink creation unavailable", err)
				}
			}
			if result, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", files); err == nil || result != nil {
				t.Fatal("unsafe state accepted", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("publication escaped private state", err)
			}
		})
	}
}

func TestGeneratedPublicationAcrossProcesses(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	commands := make([]chan []byte, 4)
	for i := range commands {
		commands[i] = make(chan []byte, 1)
		command := helper(t, directory, "generated", "same")
		go func() {
			data, err := command.CombinedOutput()
			if err != nil {
				t.Errorf("generated helper: %v %s", err, data)
			}
			commands[i] <- data
		}()
	}
	var previous map[string]string
	for _, done := range commands {
		var paths map[string]string
		if err := json.NewDecoder(bytes.NewReader(<-done)).Decode(&paths); err != nil {
			t.Fatal(err)
		}
		if previous != nil && !reflect.DeepEqual(paths, previous) {
			t.Fatal("concurrent identical consumers created different snapshots")
		}
		if string(readGenerated(t, paths["app.json"])) != "same" || string(readGenerated(t, paths["other.txt"])) != "same" {
			t.Fatal("partial generation")
		}
		previous = paths
	}
}

func readGenerated(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // Test-owned generated path.
	if err != nil {
		t.Fatal(err)
	}
	return data
}

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

func TestKilledGeneratedWriterCanRecover(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	command := helper(t, directory, "generated-churn", "crash")
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Error(err)
			}
			if err := command.Wait(); err == nil {
				t.Error("terminated writer unexpectedly succeeded")
			}
		}
	})
	var first map[string]string
	if err := json.NewDecoder(output).Decode(&first); err != nil {
		t.Fatal("writer readiness", err)
	}
	consumer := filepath.Dir(filepath.Dir(first["a.bin"]))
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	observed := false
	for !observed {
		entries, err := os.ReadDir(consumer)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".staging-") {
				observed = true
				break
			}
		}
		if !observed {
			select {
			case <-deadline.C:
				t.Fatal("writer never reached staging")
			case <-tick.C:
			}
		}
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed writer succeeded")
	}
	var index struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(readGenerated(t, filepath.Join(consumer, "current.json")), &index); err != nil {
		t.Fatal("interrupted writer damaged pointer", err)
	}
	if !bytes.Equal(readGenerated(t, filepath.Join(consumer, index.Generation, "a.bin")), readGenerated(t, filepath.Join(consumer, index.Generation, "b.bin"))) {
		t.Fatal("pointer selected a partial file set")
	}
	entries, err := os.ReadDir(consumer)
	if err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".staging-") {
			leftovers = append(leftovers, entry.Name())
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store := state.Store{Directory: directory}
	recovered, err := store.PublishGenerated(ctx, generatedWorkspace, "", "app", map[string][]byte{"a.bin": []byte("recovered"), "b.bin": []byte("recovered")})
	if err != nil {
		t.Fatal("retry failed or lock leaked", err)
	}
	if string(readGenerated(t, recovered["a.bin"])) != "recovered" {
		t.Fatal("retry selected crash staging")
	}
	for _, name := range leftovers {
		if _, err := os.Stat(filepath.Join(consumer, name)); err != nil {
			t.Fatal("retry deleted uncertain staging", err)
		}
	}
	if len(readGenerated(t, first["a.bin"])) != 1<<20 {
		t.Fatal("original generation removed")
	}
}
