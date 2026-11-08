package state_test

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func TestPortClaimIsProcessOwnedAndDoesNotHoldRegistry(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	command := helper(t, directory, "claim", "claim")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	}()
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			if err := command.Process.Kill(); err != nil {
				t.Error(err)
			}
			if err := command.Wait(); err == nil {
				t.Error("killed helper succeeded")
			}
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatal("claim handshake", line, err)
	}
	store := state.Store{Directory: directory}
	lease, acquired, err := store.TryClaimPort(t.Context(), 34567)
	if lease != nil || acquired || err != nil {
		t.Fatal("concurrent process claimed same port", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := store.Update(ctx, func(*state.Registry) error { return nil }); err != nil {
		t.Fatal("port lease held registry lock", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed child succeeded")
	}
	lease, acquired, err = store.TryClaimPort(t.Context(), 34567)
	if err != nil || !acquired {
		t.Fatal("dead process retained claim", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPortPreferencesValidateAndPreserveRegistration(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store := state.Store{Directory: directory}
	registration, err := lifecycle.Register(t.Context(), store, fakeCheckout(t, directory, "ports"), "daemon")
	if err != nil {
		t.Fatal(err)
	}
	preference := state.PortPreference{WorkspaceID: registration.ID, TargetID: "daemon", Service: "api", ContainerPort: 8000, HostPort: 24001}
	if err := store.RememberPorts(t.Context(), []state.PortPreference{preference}); err != nil {
		t.Fatal(err)
	}
	preference.HostPort = 24002
	if err := store.RememberPorts(t.Context(), []state.PortPreference{preference}); err != nil {
		t.Fatal(err)
	}
	registry, err := store.Read()
	if err != nil || len(registry.Workspaces) != 1 || len(registry.Ports) != 1 || registry.Ports[0] != preference {
		t.Fatal("preferences lost identity or duplicated endpoint", err)
	}
	before := string(readRegistry(t, directory))
	for _, invalid := range []state.PortPreference{
		{WorkspaceID: registration.ID, TargetID: "other", Service: "api", ContainerPort: 8000, HostPort: 24001},
		{WorkspaceID: registration.ID, TargetID: "daemon", Service: "api", ContainerPort: 8000},
	} {
		if err := store.RememberPorts(t.Context(), []state.PortPreference{invalid}); err == nil {
			t.Fatal("invalid preference accepted")
		}
		if string(readRegistry(t, directory)) != before {
			t.Fatal("rejected preference rewrote registry")
		}
	}
}

func TestPortAssignmentsValidateAndRemoveWithTarget(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store := state.Store{Directory: directory}
	first, err := lifecycle.Register(t.Context(), store, fakeCheckout(t, directory, "assignments-first"), "daemon")
	if err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Register(t.Context(), store, fakeCheckout(t, directory, "assignments-second"), "daemon")
	if err != nil {
		t.Fatal(err)
	}
	assignment := state.PortAssignment{WorkspaceID: first.ID, TargetID: "daemon", Service: "api", ContainerPort: 8000, HostPort: 24001}
	if err := store.AssignPorts(t.Context(), []state.PortAssignment{assignment}); err != nil {
		t.Fatal(err)
	}
	assignment.HostPort = 24002
	if err := store.AssignPorts(t.Context(), []state.PortAssignment{assignment}); err != nil {
		t.Fatal(err)
	}
	registry, err := store.Read()
	if err != nil || len(registry.Assignments) != 1 || registry.Assignments[0] != assignment {
		t.Fatal("assignment lost identity or duplicated endpoint", err)
	}
	before := string(readRegistry(t, directory))
	for _, invalid := range []state.PortAssignment{
		{WorkspaceID: first.ID, TargetID: "other", Service: "api", ContainerPort: 8000, HostPort: 24003},
		{WorkspaceID: first.ID, TargetID: "daemon", Service: "api", ContainerPort: 8000},
	} {
		if err := store.AssignPorts(t.Context(), []state.PortAssignment{invalid}); err == nil {
			t.Fatal("invalid assignment accepted")
		}
		if string(readRegistry(t, directory)) != before {
			t.Fatal("rejected assignment rewrote registry")
		}
	}
	conflict := state.PortAssignment{WorkspaceID: second.ID, TargetID: "daemon", Service: "api", ContainerPort: 8000, HostPort: assignment.HostPort}
	if err := store.AssignPorts(t.Context(), []state.PortAssignment{conflict}); !errors.Is(err, state.ErrAssignmentConflict) {
		t.Fatalf("conflicting assignment error = %v", err)
	}
	if string(readRegistry(t, directory)) != before {
		t.Fatal("conflicting assignment rewrote registry")
	}
	if err := store.Update(t.Context(), func(registry *state.Registry) error {
		registry.RemoveTarget(first.ID, "daemon")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	registry, err = store.Read()
	if err != nil || len(registry.Assignments) != 0 {
		t.Fatalf("assignment survived target removal: %+v, %v", registry.Assignments, err)
	}
}
