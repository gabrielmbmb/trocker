//go:build integration

package lifecycle_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
	"github.com/gabrielmbmb/trocker/internal/workspace"
)

func TestConcurrentAllocatedStartupAcrossProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	main, store, registration, target := upWorkspace(t, ctx)
	writeSelfBinding(t, main)
	_, other := repository(t)
	for name, content := range map[string]string{"compose.yaml": upCompose, "trocker.yaml": selfBindingManifest} {
		if err := os.WriteFile(filepath.Join(other, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	second, err := lifecycle.Init(ctx, other, store, func(lifecycle.InitProposal) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanupProject(t, cleanup, target, second)
	})
	held := allocatedProcess(t, ctx, main, store.Directory, true)
	stdout, err := held.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := held.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	}()
	held.Stderr = os.Stderr
	if err := held.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if held.ProcessState == nil {
			if err := held.Process.Kill(); err != nil {
				t.Error(err)
			}
			if err := held.Wait(); err == nil {
				t.Error("killed helper succeeded")
			}
		}
	})
	decoder := json.NewDecoder(stdout)
	var port uint16
	if err := decoder.Decode(&port); err != nil || port == 0 {
		t.Fatal("pending-port handshake", err)
	}
	// A stale preference may equal another operation's not-yet-bound candidate.
	// The held operation must block this port, not the whole registry or daemon.
	if err := store.RememberPorts(ctx, []state.PortPreference{{WorkspaceID: second.ID, TargetID: target.ID, Service: "api", ContainerPort: 8000, HostPort: port}}); err != nil {
		t.Fatal(err)
	}
	running := allocatedProcess(t, ctx, other, store.Directory, false)
	data, err := running.Output()
	if err != nil {
		t.Fatalf("independent startup blocked or failed: %v", err)
	}
	var otherResult lifecycle.EndpointResult
	if err := json.NewDecoder(strings.NewReader(string(data))).Decode(&otherResult); err != nil {
		t.Fatalf("helper output: %q %v", data, err)
	}
	if otherResult.Endpoints[0].Addresses.Host.Port == port {
		t.Fatal("processes selected the same port")
	}
	readEndpointToken(t, ctx, otherResult)
	if _, err := fmt.Fprintln(stdin, "continue"); err != nil {
		t.Fatal(err)
	}
	var firstResult lifecycle.EndpointResult
	if err := decoder.Decode(&firstResult); err != nil {
		t.Fatal(err)
	}
	if err := held.Wait(); err != nil {
		t.Fatal(err)
	}
	if firstResult.Endpoints[0].Addresses.Host.Port != port || firstResult.WorkspaceID != registration.ID {
		t.Fatal("pending allocation was stolen")
	}
	readEndpointToken(t, ctx, firstResult)
	registry, err := store.Read()
	if err != nil || len(registry.Ports) != 2 {
		t.Fatal("concurrent preferences lost", err)
	}
}

func allocatedProcess(t *testing.T, ctx context.Context, main, directory string, hold bool) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestAllocatedStartupProcessHelper$") //nolint:gosec // Runs this test binary with a fixed helper entry point.
	command.Env = append(os.Environ(), "TROCKER_TEST_ALLOCATION_MAIN="+main, "TROCKER_TEST_ALLOCATION_STATE="+directory, "TROCKER_TEST_ALLOCATION_HOLD="+strconv.FormatBool(hold))
	return command
}

func TestAllocatedStartupProcessHelper(t *testing.T) {
	main := os.Getenv("TROCKER_TEST_ALLOCATION_MAIN")
	if main == "" {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	store := state.Store{Directory: os.Getenv("TROCKER_TEST_ALLOCATION_STATE")}
	output := io.Discard
	if os.Getenv("TROCKER_TEST_ALLOCATION_HOLD") == "true" {
		preview, err := lifecycle.Plan(ctx, main, store)
		if err != nil {
			t.Fatal(err)
		}
		target, err := compose.ResolveTarget(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := target.Close(); err != nil {
				t.Error(err)
			}
		}()
		output = &holdAllocatedPort{ctx: ctx, target: target, registration: preview.Registration}
	}
	result, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
}

type holdAllocatedPort struct {
	ctx          context.Context
	target       *compose.Target
	registration workspace.Registration
	held         bool
}

func (w *holdAllocatedPort) Write(data []byte) (int, error) {
	if w.held || !strings.Contains(string(data), w.registration.ProjectName()+"-api-1 Created") {
		return len(data), nil
	}
	w.held = true
	inspected, err := w.target.Client.ContainerInspect(w.ctx, w.registration.ProjectName()+"-api-1", client.ContainerInspectOptions{})
	if err != nil {
		return 0, err
	}
	if inspected.Container.Config.Labels[compose.WorkspaceLabel] != w.registration.ID {
		return 0, fmt.Errorf("unexpected fixture ownership")
	}
	port, err := network.ParsePort("8000/tcp")
	if err != nil {
		return 0, err
	}
	published, err := strconv.ParseUint(inspected.Container.HostConfig.PortBindings[port][0].HostPort, 10, 16)
	if err != nil {
		return 0, err
	}
	if err := json.NewEncoder(os.Stdout).Encode(published); err != nil {
		return 0, err
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return 0, err
	}
	return len(data), nil
}
