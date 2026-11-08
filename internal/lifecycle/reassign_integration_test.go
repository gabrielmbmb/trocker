//go:build integration

package lifecycle_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/plan"
)

func TestReassignPortsMovesOnlyStoppedPublications(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	main, store, _, _, hostPort := stableWorkspace(t, ctx)
	api := []plan.PortKey{{Service: "api", Port: 8000}}
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}

	if _, err := lifecycle.ReassignPorts(ctx, main, store, api); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("reassign while running = %v", err)
	}
	if ports, err := lifecycle.Ports(ctx, main, store); err != nil || ports.Ports[0].HostPort != hostPort {
		t.Fatalf("refused reassign changed the assignment: %+v, %v", ports, err)
	}

	if err := lifecycle.ApplyLifecycle(ctx, main, store, compose.LifecycleOptions{Operation: compose.Stop}, io.Discard); err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.ReassignPorts(ctx, main, store, api)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Moved) != 1 || result.Moved[0].From != hostPort || result.Moved[0].To == hostPort || !config.DefaultStablePortRange.Contains(result.Moved[0].To) {
		t.Fatalf("moves = %+v, from %d", result.Moved, hostPort)
	}
	moved := result.Moved[0].To
	if ports, err := lifecycle.Ports(ctx, main, store); err != nil || ports.Ports[0].HostPort != moved {
		t.Fatalf("reassignment was not persisted: %+v, %v", ports, err)
	}
	restarted, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Endpoints[0].Addresses.Host == nil || restarted.Endpoints[0].Addresses.Host.Port != moved {
		t.Fatalf("up did not publish the reassigned port: %+v", restarted.Endpoints)
	}
	readEndpointToken(t, ctx, restarted)
}
