package state_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestReadDoesNotCreateState(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "absent")
	registry, err := (state.Store{Directory: directory}).Read()
	if err != nil || registry.Version != 1 || len(registry.Workspaces) != 0 {
		t.Fatalf("read = %+v, %v", registry, err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created state")
	}
}

func TestInvalidRegistryIsPreserved(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"", "null", "{", `{"version":99}`, `{"version":1,"unknown":true}`, `{"version":1} {"version":1}`, `{"version":1,"workspaces":[{"id":"bad"}]}`} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "registry.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			store := state.Store{Directory: directory}
			if err := store.Update(t.Context(), func(*state.Registry) error { t.Fatal("invalid registry reached updater"); return nil }); err == nil {
				t.Fatal("invalid registry accepted")
			}
			after, err := os.ReadFile(path) //nolint:gosec // Test-owned registry path.
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != data {
				t.Fatal("invalid registry overwritten")
			}
		})
	}
}

func TestRegistrationRoundTripAndPermissions(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "state")
	store := state.Store{Directory: directory}
	checkout := fakeCheckout(t, t.TempDir(), "one")
	first, err := lifecycle.Register(t.Context(), store, checkout, "daemon-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Register(t.Context(), store, checkout, "daemon-one")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.ProjectName() != second.ProjectName() {
		t.Fatal("re-registration changed identity")
	}
	third, err := lifecycle.Register(t.Context(), store, checkout, "daemon-two")
	if err != nil {
		t.Fatal(err)
	}
	if third.ID != first.ID || len(third.Targets) != 2 {
		t.Fatal("target association lost identity or old target")
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].ID != first.ID {
		t.Fatalf("registry = %+v, %v", registry, err)
	}
	before := readRegistry(t, directory)
	expected := errors.New("abort")
	if err := store.Update(t.Context(), func(r *state.Registry) error { r.Workspaces = nil; return expected }); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	if !bytes.Equal(before, readRegistry(t, directory)) {
		t.Fatal("failed update changed registry")
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{directory, filepath.Join(directory, "locks"), filepath.Join(directory, "registry.json")} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("nonprivate state permissions: %s", path)
			}
		}
	}
}

func TestConcurrentRegistrationsAcrossProcesses(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	var wait sync.WaitGroup
	for i := range 6 {
		wait.Go(func() {
			// Two processes register each checkout; each pair must converge on one UUID.
			command := helper(t, directory, "register", fmt.Sprint(i%3))
			if output, err := command.CombinedOutput(); err != nil {
				t.Errorf("child failed: %v\n%s", err, output)
			}
		})
	}
	wait.Wait()
	registry, err := (state.Store{Directory: directory}).Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Workspaces) != 3 {
		t.Fatalf("lost updates or duplicate registrations: %+v", registry)
	}
	ids := map[string]bool{}
	for _, registration := range registry.Workspaces {
		if ids[registration.ID] {
			t.Fatal("independent checkouts share identity")
		}
		ids[registration.ID] = true
	}
}

func TestKilledWriterAndLockRecovery(t *testing.T) {
	for _, mode := range []string{"hold", "churn"} {
		t.Run(mode, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			store := state.Store{Directory: directory}
			first, err := lifecycle.Register(t.Context(), store, fakeCheckout(t, directory, "shared"), "initial")
			if err != nil {
				t.Fatal(err)
			}
			command := helper(t, directory, mode, "shared")
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := input.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
					t.Error(err)
				}
			})
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if command.ProcessState == nil {
					if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
						t.Error(err)
					}
					if err := command.Wait(); err == nil {
						t.Error("terminated helper unexpectedly succeeded")
					}
				}
			})
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
				t.Fatalf("child readiness: %q, %v", line, err)
			}
			if mode == "hold" {
				before := readRegistry(t, directory)
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				err := store.Update(ctx, func(*state.Registry) error { t.Error("entered locked update"); return nil })
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lock cancellation: %v", err)
				}
				if !bytes.Equal(before, readRegistry(t, directory)) {
					t.Fatal("cancelled update changed registry")
				}
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err == nil {
				t.Fatal("killed child succeeded")
			}
			registry, err := store.Read()
			if err != nil {
				t.Fatal("interrupted writer left invalid registry: ", err)
			}
			if len(registry.Workspaces) != 1 || registry.Workspaces[0].ID != first.ID {
				t.Fatal("interruption lost identity")
			}
			recovered, err := lifecycle.Register(t.Context(), store, fakeCheckout(t, directory, "shared"), "after-crash")
			if err != nil || recovered.ID != first.ID {
				t.Fatalf("lock did not recover: %+v, %v", recovered, err)
			}
		})
	}
}

func TestUnsafeStatePathIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions and symlink test")
	}
	directory := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "registry.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := (state.Store{Directory: directory}).Read(); err == nil {
		t.Fatal("symlink registry accepted")
	}
	if err := os.Chmod(directory, 0o755); err != nil { //nolint:gosec // Deliberately insecure fixture to verify rejection.
		t.Fatal(err)
	}
	if _, err := (state.Store{Directory: directory}).Read(); err == nil {
		t.Fatal("nonprivate state directory accepted")
	}
}

func TestDefaultDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "Local"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	got, err := state.DefaultDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "state", "trocker")
	switch runtime.GOOS {
	case "darwin":
		want = filepath.Join(home, "Library", "Application Support", "trocker")
	case "windows":
		want = filepath.Join(home, "Local", "Trocker")
	}
	if got != want {
		t.Fatalf("directory = %s, want %s", got, want)
	}
}

// The child uses the same public registration path as initialization. Handshakes
// coordinate interruption; the parent never sleeps to guess lock ownership.
func TestStateProcessHelper(t *testing.T) {
	mode := os.Getenv("TROCKER_TEST_STATE_MODE")
	if mode == "" {
		return
	}
	store := state.Store{Directory: os.Getenv("TROCKER_TEST_STATE_DIR")}
	checkout := fakeCheckout(t, store.Directory, os.Getenv("TROCKER_TEST_STATE_KEY"))
	switch mode {
	case "generated-churn":
		for i := 0; ; i++ {
			value := bytes.Repeat([]byte{byte(i % 256)}, 1<<20)
			paths, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", map[string][]byte{"a.bin": value, "b.bin": value})
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				if err := json.NewEncoder(os.Stdout).Encode(paths); err != nil {
					t.Fatal(err)
				}
			}
		}
	case "generated":
		value := []byte(os.Getenv("TROCKER_TEST_STATE_KEY"))
		paths, err := store.PublishGenerated(t.Context(), generatedWorkspace, "", "app", map[string][]byte{"app.json": value, "other.txt": value})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(paths); err != nil {
			t.Fatal(err)
		}
	case "register":
		if _, err := lifecycle.Register(t.Context(), store, checkout, "daemon"); err != nil {
			t.Fatal(err)
		}
	case "claim":
		lease, acquired, err := store.TryClaimPort(t.Context(), 34567)
		if err != nil || !acquired {
			t.Fatalf("claim: %v %v", acquired, err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
			t.Fatal(err)
		}
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	case "hold":
		err := store.Update(t.Context(), func(*state.Registry) error {
			if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
				return err
			}
			_, err := bufio.NewReader(os.Stdin).ReadString('\n')
			return err // Parent keeps the pipe open, then kills the lock owner.
		})
		if err != nil {
			t.Fatal(err)
		}
	case "churn":
		for i := 0; ; i++ {
			if err := store.Update(t.Context(), func(r *state.Registry) error {
				r.Workspaces[0].Targets = []string{strings.Repeat("x", 65536), fmt.Sprint(i)}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func helper(t *testing.T, directory, mode, key string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestStateProcessHelper$") //nolint:gosec // Executes this test binary with a fixed helper test.
	command.Env = append(os.Environ(), "TROCKER_TEST_STATE_MODE="+mode, "TROCKER_TEST_STATE_DIR="+directory, "TROCKER_TEST_STATE_KEY="+key)
	return command
}

func fakeCheckout(t *testing.T, directory, key string) workspace.Checkout {
	t.Helper()
	root := filepath.Join(directory, "checkout-"+key)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil { //nolint:gosec // Subprocess paths and keys are supplied by the parent test's temporary fixtures.
		t.Fatal(err)
	}
	return workspace.Checkout{Root: root, GitDir: filepath.Join(root, ".git"), CommonDir: filepath.Join(root, ".git")}
}

func readRegistry(t *testing.T, directory string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "registry.json")) //nolint:gosec // Test-owned state directory.
	if err != nil {
		t.Fatal(err)
	}
	return data
}
