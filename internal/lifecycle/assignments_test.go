package lifecycle

import (
	"reflect"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func TestChooseStablePortPrefersCurrentAndAvoidsUsed(t *testing.T) {
	used := map[uint16]bool{24002: true, 24003: true}
	port, err := chooseStablePort(t.Context(), used, 24001, 24002, 24003)
	if err != nil || port != 24001 {
		t.Fatalf("current candidate = %d, %v", port, err)
	}
	port, err = chooseStablePort(t.Context(), used, 0, 24002, 24003)
	if err != nil || port == 0 || used[port] {
		t.Fatalf("free candidate = %d, %v", port, err)
	}
}

func TestAssignmentMapAndStoredAssignments(t *testing.T) {
	registry := state.Registry{Assignments: []state.PortAssignment{
		{WorkspaceID: "one", TargetID: "daemon", Service: "web", ContainerPort: 3000, HostPort: 24003},
		{WorkspaceID: "other", TargetID: "daemon", Service: "api", ContainerPort: 8000, HostPort: 24001},
		{WorkspaceID: "one", TargetID: "other", Service: "api", ContainerPort: 8000, HostPort: 24002},
		{WorkspaceID: "one", TargetID: "daemon", Service: "api", ContainerPort: 8000, HostPort: 24001},
	}}
	assignments := assignmentMap(registry, "one", "daemon")
	expected := map[plan.PortKey]uint16{
		{Service: "api", Port: 8000}: 24001,
		{Service: "web", Port: 3000}: 24003,
	}
	if !reflect.DeepEqual(assignments, expected) {
		t.Fatalf("assignments = %v", assignments)
	}
	stored := storedAssignments(registry, "one", "daemon")
	if len(stored) != 2 || stored[0].Service != "api" || stored[1].Service != "web" {
		t.Fatalf("stored assignments = %+v", stored)
	}
}
