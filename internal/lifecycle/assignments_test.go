package lifecycle

import (
	"reflect"
	"slices"
	"testing"

	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/plan"
	"github.com/gabrielmbmb/trocker/internal/state"
)

func TestSeedPort(t *testing.T) {
	t.Parallel()
	ephemeral := config.PortRange{First: 49152, Last: 65535}
	busy := map[uint16]bool{8001: true, 50002: true}
	available := func(port uint16) (bool, error) { return !busy[port], nil }
	for _, test := range []struct {
		name string
		seed stableSeed
		used map[uint16]bool
		want uint16
	}{
		{name: "current port is kept without probing", seed: stableSeed{current: 8001, preference: 8002, compose: 8003}, want: 8001},
		{name: "dynamic ports inside the ephemeral range move", seed: stableSeed{current: 50000, preference: 50001, compose: 8003}, want: 8003},
		{name: "explicit Compose port inside the ephemeral range is kept", seed: stableSeed{current: 50000, compose: 50003}, want: 50003},
		{name: "Compose port the container already publishes is kept", seed: stableSeed{current: 50002, compose: 50002}, want: 50002},
		{name: "used and busy seeds leave the range to choose", seed: stableSeed{preference: 8002, compose: 8001}, used: map[uint16]bool{8002: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			port, err := seedPort(test.seed, test.used, ephemeral, available)
			if err != nil || port != test.want {
				t.Fatalf("seedPort = %d, %v; want %d", port, err, test.want)
			}
		})
	}
}

func TestPoolPorts(t *testing.T) {
	t.Parallel()
	pool := config.PortRange{First: 20000, Last: 20005}
	for _, test := range []struct {
		name  string
		taken []uint16
		count int
		want  []uint16
	}{
		{name: "contiguous run is preferred over an earlier hole", taken: []uint16{20001}, count: 3, want: []uint16{20002, 20003, 20004}},
		{name: "without a run the first free ports are used, through the last", taken: []uint16{20001, 20003}, count: 4, want: []uint16{20000, 20002, 20004, 20005}},
		{name: "too few free ports", taken: []uint16{20000, 20001, 20002, 20003}, count: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			taken := func(port uint16) (bool, error) { return slices.Contains(test.taken, port), nil }
			ports, err := poolPorts(test.count, pool, taken)
			if err != nil || !slices.Equal(ports, test.want) {
				t.Fatalf("poolPorts = %v, %v; want %v", ports, err, test.want)
			}
		})
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
