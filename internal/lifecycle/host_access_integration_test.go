//go:build integration

package lifecycle_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/gabrielmbmb/trocker/internal/compose"
	"github.com/gabrielmbmb/trocker/internal/config"
	"github.com/gabrielmbmb/trocker/internal/lifecycle"
	"github.com/gabrielmbmb/trocker/internal/state"
)

const hostAccessCompose = `services:
  api:
    image: busybox:1.37
    pid: host
    ipc: host
    cap_add: [SYS_PTRACE]
    devices: ['/dev/null:/dev/null:rw']
    command: [sleep, '300']
`

func TestUpAppliesDeclaredHostAccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	directory, main := repository(t)
	store := state.Store{Directory: filepath.Join(directory, "state")}
	manifest := "version: 1\ncompose: [compose.yaml]\nhost_access:\n  api:\n    pid: true\n    ipc: true\n    capabilities: [SYS_PTRACE]\n    devices:\n      - source: /dev/null\n        permissions: rw\n"
	for name, content := range map[string]string{"compose.yaml": hostAccessCompose, "trocker.yaml": manifest} {
		if err := os.WriteFile(filepath.Join(main, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registration, err := lifecycle.Init(ctx, main, store, func(lifecycle.InitProposal) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	target, err := compose.ResolveTarget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanupProject(t, cleanup, target, registration)
		if err := target.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := lifecycle.Up(ctx, main, store, compose.UpOptions{Detach: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	containers, err := target.Containers(ctx, registration)
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers: %v %v", containers, err)
	}
	inspected, err := target.Client.ContainerInspect(ctx, containers[0].ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	host := inspected.Container.HostConfig
	if host == nil {
		t.Fatal("container has no host config")
	}
	if host.PidMode != "host" || host.IpcMode != "host" {
		t.Fatalf("host namespaces = %q, %q, want host, host", host.PidMode, host.IpcMode)
	}
	found := false
	for _, capability := range host.CapAdd {
		if config.NormalizeCapability(capability) == "SYS_PTRACE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("capabilities = %#v, want SYS_PTRACE", host.CapAdd)
	}
	found = false
	for _, device := range host.Devices {
		if device.PathOnHost == "/dev/null" && device.CgroupPermissions == "rw" {
			found = true
		}
	}
	if !found {
		t.Fatalf("devices = %#v, want /dev/null with rw", host.Devices)
	}
}
